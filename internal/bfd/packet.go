// Package bfd 实现 RFC 5880 双向转发检测(Bidirectional Forwarding Detection)
// 协议的核心:控制报文编解码与合法性校验、三态状态机、定时器协商、
// 轮询/终止(Poll/Final)序列、回声模式与按需模式。
//
// 本实现运行在可控的虚拟时钟仿真环境之上(见 internal/sim),所有定时器与
// 重传行为都是事件驱动的,因此同一种子下结果完全可复现。
package bfd

import (
	"errors"
)

// ---- 协议常量(RFC 5880 Section 6.8.6 等) ----

const (
	Version        = 1          // BFD 协议版本号,合法值恒为 1
	MinHdrLen      = 24         // 无认证字段时控制报文最小长度
	MaxHdrLen      = 24         // 本实现不支持认证,最大长度即最小长度
	EchoPktLen     = 24         // 本实现定义的回声报文长度
	EchoMagic      = 0x42464445 // "BFDE" 回声报文魔数
	DemandSlowRate = 1          // 按需模式下异步控制报文的最低发送频率(秒/包)
)

// State 是 BFD 会话状态。对端可见的状态通过控制报文 Sta 字段传递。
type State uint8

const (
	StateAdminDown State = 0 // 管理关闭:本端主动暂停会话
	StateDown      State = 1 // 关闭:会话未建立
	StateInit      State = 2 // 初始:已收到对端 Down,等待 Up
	StateUp        State = 3 // 建立:双向连通
)

func (s State) String() string {
	switch s {
	case StateAdminDown:
		return "AdminDown"
	case StateDown:
		return "Down"
	case StateInit:
		return "Init"
	case StateUp:
		return "Up"
	}
	return "Unknown"
}

// Diag 是控制报文 Diag 字段(诊断码),描述本端进入 Down/AdminDown 的原因。
type Diag uint8

const (
	DiagNone              Diag = 0 // 无诊断
	DiagControlDetectExp  Diag = 1 // 控制检测时间超时
	DiagEchoFailed        Diag = 2 // 回声功能失效
	DiagNeighborSignaled  Diag = 3 // 对端通知会话关闭
	DiagForwardingReset   Diag = 4 // 转发面复位
	DiagPathDown          Diag = 5 // 通道故障
	DiagConcatPathDown    Diag = 6 // 串联通道故障
	DiagAdminDownReason   Diag = 7 // 管理关闭
	DiagReverseConcatDown Diag = 8 // 反向串联通道故障
)

func (d Diag) String() string {
	switch d {
	case DiagNone:
		return "No Diagnostic"
	case DiagControlDetectExp:
		return "Control Detection Time Expired"
	case DiagEchoFailed:
		return "Echo Function Failed"
	case DiagNeighborSignaled:
		return "Neighbor Signaled Session Down"
	case DiagForwardingReset:
		return "Forwarding Plane Reset"
	case DiagPathDown:
		return "Path Down"
	case DiagConcatPathDown:
		return "Concatenated Path Down"
	case DiagAdminDownReason:
		return "Administratively Down"
	case DiagReverseConcatDown:
		return "Reverse Concatenated Path Down"
	}
	return "Unknown"
}

// ControlPacket 是 BFD 控制报文(RFC 5880 Section 6.8.6)的结构化表示。
// 所有时间量单位均为纳秒,在报文中按微秒序列化。
type ControlPacket struct {
	Version         uint8  // 版本号,必须为 1
	Diag            Diag   // 诊断码
	State           State  // 本端会话状态(Sta)
	Poll            bool   // P 位:请求对端确认/应答
	Final           bool   // F 位:对 P 位的应答
	ControlPlane    bool   // C 位:本端控制平面独立性
	AuthPresent     bool   // A 位:是否携带认证(本实现不支持,置位即非法)
	Demand          bool   // D 位:本端期望使用按需模式
	Multipoint      bool   // M 位:多点位,必须为 0
	DetectMult      uint8  // 检测倍数
	Length          uint8  // 报文总长度(字节)
	MyDisc          uint32 // 本端鉴别符
	YourDisc        uint32 // 对端鉴别符(未学习前为 0)
	DesiredMinTx    int64  // 期望最小发包间隔(纳秒)
	RequiredMinRx   int64  // 要求最小收包间隔(纳秒)
	RequiredMinEcho int64  // 要求最小回声间隔(纳秒)
}

// Encode 将控制报文序列化为线上字节流(24 字节定长)。
func (p *ControlPacket) Encode() []byte {
	b := make([]byte, MinHdrLen)
	b[0] = byte(Version<<5) | byte(p.Diag&0x1f)
	b[1] = uint8(p.State&0x3) << 6
	if p.Poll {
		b[1] |= 1 << 5
	}
	if p.Final {
		b[1] |= 1 << 4
	}
	if p.ControlPlane {
		b[1] |= 1 << 3
	}
	if p.AuthPresent {
		b[1] |= 1 << 2
	}
	if p.Demand {
		b[1] |= 1 << 1
	}
	if p.Multipoint {
		b[1] |= 1
	}
	b[2] = p.DetectMult
	b[3] = MinHdrLen
	putU32(b[4:8], p.MyDisc)
	putU32(b[8:12], p.YourDisc)
	putU32(b[12:16], uint32(p.DesiredMinTx/1000))
	putU32(b[16:20], uint32(p.RequiredMinRx/1000))
	putU32(b[20:24], uint32(p.RequiredMinEcho/1000))
	return b
}

// DropReason 标识报文被丢弃的具体合法性原因,用于分类计数。
type DropReason string

const (
	DropTooShort      DropReason = "报文长度不足 24 字节"
	DropBadVersion    DropReason = "版本号不为 1"
	DropBadLength     DropReason = "Length 字段与实际长度不符"
	DropAuthPresent   DropReason = "携带不支持的认证字段(A=1)"
	DropMultipointSet DropReason = "多点位 M 非法置 1"
	DropPollFinalBoth DropReason = "P 位与 F 位同时置 1"
	DropBadDetectMult DropReason = "检测倍数为 0"
	DropZeroMyDisc    DropReason = "本端鉴别符为 0"
	DropZeroYourDisc  DropReason = "Up 状态报文中 Your Discriminator 为 0"
	DropEchoMagic     DropReason = "回声报文魔数错误"
	DropEchoTooShort  DropReason = "回声报文长度不足"
)

// ParseControl 解析并完整校验控制报文。任何字段不合法都返回对应 DropReason,
// 由调用方直接丢弃并计数,不进入状态机。
func ParseControl(b []byte) (*ControlPacket, DropReason) {
	if len(b) < MinHdrLen {
		return nil, DropTooShort
	}
	ver := b[0] >> 5
	if ver != Version {
		return nil, DropBadVersion
	}
	p := &ControlPacket{
		Version:         ver,
		Diag:            Diag(b[0] & 0x1f),
		State:           State((b[1] >> 6) & 0x3),
		Poll:            b[1]&(1<<5) != 0,
		Final:           b[1]&(1<<4) != 0,
		ControlPlane:    b[1]&(1<<3) != 0,
		AuthPresent:     b[1]&(1<<2) != 0,
		Demand:          b[1]&(1<<1) != 0,
		Multipoint:      b[1]&1 != 0,
		DetectMult:      b[2],
		Length:          b[3],
		MyDisc:          getU32(b[4:8]),
		YourDisc:        getU32(b[8:12]),
		DesiredMinTx:    int64(getU32(b[12:16])) * 1000,
		RequiredMinRx:   int64(getU32(b[16:20])) * 1000,
		RequiredMinEcho: int64(getU32(b[20:24])) * 1000,
	}
	if int(p.Length) != len(b) {
		return nil, DropBadLength
	}
	if p.AuthPresent {
		return nil, DropAuthPresent // 本实现不支持认证
	}
	if p.Multipoint {
		return nil, DropMultipointSet // RFC 5880 §6.8.6:M 必须为 0
	}
	if p.Poll && p.Final {
		return nil, DropPollFinalBoth // P/F 互斥
	}
	if p.DetectMult == 0 {
		return nil, DropBadDetectMult
	}
	if p.MyDisc == 0 {
		return nil, DropZeroMyDisc
	}
	if p.State == StateUp && p.YourDisc == 0 {
		return nil, DropZeroYourDisc
	}
	return p, ""
}

// EchoPacket 是回声报文(RFC 5880 Section 6.8.7)的本地实现格式。
// 回声报文由本端构造、对端原样回送,内容对转发方不透明;这里采用带魔数与
// 发送方鉴别符的定长结构,便于对端回送时按鉴别符找到归属会话。
type EchoPacket struct {
	Magic    uint32
	Sender   uint32 // 发送方(归属会话)鉴别符
	Seq      uint32 // 序列号
	SendTime int64  // 发送时刻(虚拟纳秒),回送后用于测量往返
}

// Encode 序列化回声报文。
func (e *EchoPacket) Encode() []byte {
	b := make([]byte, EchoPktLen)
	putU32(b[0:4], e.Magic)
	putU32(b[4:8], e.Sender)
	putU32(b[8:12], e.Seq)
	putU32(b[12:16], uint32(e.SendTime/1000)) // 微秒,避免纳秒截断
	return b
}

// ParseEcho 解析并校验回声报文。
func ParseEcho(b []byte) (*EchoPacket, DropReason) {
	if len(b) < EchoPktLen {
		return nil, DropEchoTooShort
	}
	e := &EchoPacket{
		Magic:    getU32(b[0:4]),
		Sender:   getU32(b[4:8]),
		Seq:      getU32(b[8:12]),
		SendTime: int64(getU32(b[12:16])) * 1000, // 微秒还原为纳秒
	}
	if e.Magic != EchoMagic {
		return nil, DropEchoMagic
	}
	return e, ""
}

func putU32(b []byte, v uint32) {
	b[0] = byte(v >> 24)
	b[1] = byte(v >> 16)
	b[2] = byte(v >> 8)
	b[3] = byte(v)
}

func getU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ErrSessionClosed 在会话已结束后继续操作时返回。
var ErrSessionClosed = errors.New("bfd: session closed")

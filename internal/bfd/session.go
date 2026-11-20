package bfd

import "time"

// ---- 事件日志(供时间线页面与可复现校验使用) ----

// EventLog 记录会话生命周期中的每一个可观察事件。
type EventLog struct {
	T      int64  `json:"t"`      // 事件时刻(虚拟纳秒)
	Node   string `json:"node"`   // 节点名
	LinkID string `json:"linkId"` // 所属链路
	Type   string `json:"type"`   // 事件类型
	Detail string `json:"detail"` // 人类可读细节
}

// 事件类型常量。
const (
	EvSendControl = "send-control"
	EvRecvControl = "recv-control"
	EvSendEcho    = "send-echo"
	EvRecvEcho    = "recv-echo"
	EvStateChange = "state-change"
	EvTimeout     = "timeout"
	EvPoll        = "poll"
	EvFinal       = "final"
	EvDrop        = "drop"
	EvParamChange = "param-change"
)

// Config 是会话的本地配置参数。
type Config struct {
	Node            string // 节点名
	LinkID          string // 所属链路标识
	LocalDisc       uint32 // 本端鉴别符
	DesiredMinTx    int64  // 期望最小发包间隔(纳秒)
	RequiredMinRx   int64  // 要求最小收包间隔(纳秒)
	RequiredMinEcho int64  // 要求最小回声间隔(纳秒),>0 表示本端支持回声
	DetectMult      uint8  // 检测倍数
	EchoEnabled     bool   // 是否启用回声模式
	DemandEnabled   bool   // 是否启用按需模式(D 位)
	TxJitterPct     int    // 发包间隔随机抖动百分比(仿真用,默认 0)
	// DetectionGrace 是检测判定宽限时间。实际判死线 = 理论检测时间 + 宽限,
	// 用于吸收报文抖动、丢包恢复与事件调度的相位影响,避免在链路仍可用时
	// 误判(真实 BFD 实现普遍保留一个间隔量级的调度宽限)。默认 0。
	DetectionGrace int64
}

// Session 是单个 BFD 会话的全部运行时状态。会话本身不直接接触引擎,
// 而是通过回调(Schedule/发送函数)与仿真环境解耦。
type Session struct {
	cfg Config

	// 引擎回调。schedule 排程一次唤醒并返回定时器代次;拓扑在触发时校验
	// 代次,丢弃被后续排程取代的旧定时器,保证同一会话至多一个有效定时器。
	schedule func(after int64) uint64        // 请求在 after 纳秒后收到一次 Wake
	sendCtrl func(pkt *ControlPacket)        // 发送控制报文
	sendEcho func(e *EchoPacket)             // 发送回声报文
	emit     func(evType, detail string)     // 写事件日志
	notify   func(old, new State, diag Diag) // 状态变化通知上层

	// 状态机
	state State
	diag  Diag

	// 对端学习结果
	remoteKnown      bool
	remoteState      State
	remoteDisc       uint32
	remoteMult       uint8
	remoteDesiredTx  int64
	remoteRequiredRx int64
	remoteEchoRx     int64
	remoteDemand     bool

	// 定时器协商结果(纳秒)
	txInterval   int64 // 实际异步发送间隔
	rxInterval   int64 // 实际接收间隔(用于计算检测时间)
	echoInterval int64 // 实际回声发送间隔

	// 检测死线(虚拟纳秒);0 表示未布防
	controlDeadline int64
	echoDeadline    int64

	// 轮询/终止序列
	pollActive bool // 本端已发 P 位、等待 F 位
	pollSeq    uint32
	echoSeq    uint32

	// 待生效参数(对端确认 F 位前继续使用旧间隔)
	pendingDesiredTx    int64
	pendingRequiredRx   int64
	pendingRequiredEcho int64
	pendingMult         uint8
	hasPending          bool

	// 统计
	rxPackets      int64
	txPackets      int64
	rxEchoPackets  int64
	txEchoPackets  int64
	dropCounts     map[DropReason]int64
	lastPacketAt   int64 // 最近一次发送或接收控制报文时刻(发送门控用)
	lastRecvAt     int64 // 最近一次"接收"合法控制报文时刻(检测诊断用)
	lastEchoAt     int64 // 最近一次"发送"回声报文时刻(回声发送门控用)
	lastEchoRecvAt int64 // 最近一次"接收"回声回包时刻
	downCount      int64
	pendingFaultAt int64 // 最近一次导致判定的故障注入时刻(场景层标注)

	closed bool
	// timerGen 定时器代次。每次排程递增;拓扑侧据此丢弃被新排程取代的
	// 旧定时器,保证一个会话同时至多一个有效定时器(消除幽灵定时器)。
	timerGen uint64
}

// NewSession 构造会话,注入仿真环境回调。
func NewSession(cfg Config,
	schedule func(after int64) uint64,
	sendCtrl func(pkt *ControlPacket),
	sendEcho func(e *EchoPacket),
	emit func(evType, detail string),
	notify func(old, new State, diag Diag),
) *Session {
	s := &Session{
		cfg:        cfg,
		state:      StateDown,
		diag:       DiagNone,
		txInterval: cfg.DesiredMinTx,
		rxInterval: 0, // 尚未学到对端参数
		dropCounts: map[DropReason]int64{},
		schedule:   schedule,
		sendCtrl:   sendCtrl,
		sendEcho:   sendEcho,
		emit:       emit,
		notify:     notify,
	}
	return s
}

// LocalDisc 返回本端鉴别符。
func (s *Session) LocalDisc() uint32 { return s.cfg.LocalDisc }

// RxInterval 返回当前协商收包间隔(理论边界计算用)。
func (s *Session) RxInterval() int64 { return s.rxInterval }

// Grace 返回检测判定宽限。
func (s *Session) Grace() int64 { return s.cfg.DetectionGrace }

// EchoIntervalVal 返回当前协商回声间隔。
func (s *Session) EchoIntervalVal() int64 { return s.echoInterval }

// TimerGen 返回当前定时器代次,供拓扑校验过期定时器。
func (s *Session) TimerGen() uint64 { return s.timerGen }

// armTimer 递增代次并排程,返回最新代次。
func (s *Session) armTimer(after int64) uint64 {
	s.timerGen++
	return s.schedule(after)
}

// State 返回当前会话状态。
func (s *Session) State() State { return s.state }

// ---- 协商公式(RFC 5880 §6.8.2) ----

// effectiveTxInterval 是实际异步发包间隔:
// 协商发包间隔 = max(本端 DesiredMinTx, 对端 RequiredMinRx)。
func (s *Session) effectiveTxInterval() int64 {
	if !s.remoteKnown {
		return s.cfg.DesiredMinTx
	}
	return max64(s.cfg.DesiredMinTx, s.remoteRequiredRx)
}

// effectiveRxInterval 是期望对端的发包间隔,即本端检测使用的收包间隔:
// 协商收包间隔 = max(对端 DesiredMinTx, 本端 RequiredMinRx)。
func (s *Session) effectiveRxInterval() int64 {
	if !s.remoteKnown {
		return 0
	}
	return max64(s.remoteDesiredTx, s.cfg.RequiredMinRx)
}

// effectiveEchoInterval 是回声发送间隔 = max(本端回声要求, 对端回声要求)。
func (s *Session) effectiveEchoInterval() int64 {
	if !s.remoteKnown || s.cfg.RequiredMinEcho == 0 || s.remoteEchoRx == 0 {
		return 0
	}
	return max64(s.cfg.RequiredMinEcho, s.remoteEchoRx)
}

// DetectionTime 返回当前控制面检测时间 = 对端检测倍数 × 协商收包间隔;
// 对端参数未学习时返回 0。
func (s *Session) DetectionTime() int64 {
	if !s.remoteKnown {
		return 0
	}
	return int64(s.remoteMult) * s.effectiveRxInterval()
}

// EchoDetectionTime 返回回声检测时间 = 本端检测倍数 × 协商回声间隔。
func (s *Session) EchoDetectionTime() int64 {
	ei := s.effectiveEchoInterval()
	if ei == 0 {
		return 0
	}
	return int64(s.cfg.DetectMult) * ei
}

// demandModeActive 判断按需模式是否真正生效:需要两端都置 D 位且会话 Up。
func (s *Session) demandModeActive() bool {
	return s.cfg.DemandEnabled && s.remoteDemand && s.state == StateUp
}

// echoModeActive 判断回声检测是否生效:本端启用且两端都支持回声、会话 Up。
func (s *Session) echoModeActive() bool {
	return s.cfg.EchoEnabled && s.state == StateUp && s.effectiveEchoInterval() > 0
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---- 启动与定时驱动 ----

// Start 启动会话:从 Down 状态开始发送 Down 报文并完成三次握手。
func (s *Session) Start(now int64) {
	s.txInterval = s.cfg.DesiredMinTx
	s.armTimer(0)
}

// Wake 是引擎定时器到期入口,now 为当前虚拟时间。
func (s *Session) Wake(now int64) {
	if s.closed {
		return
	}

	// 1. 检测超时判定(先判定再发送,语义清晰)
	s.checkTimeouts(now)
	if s.closed {
		return
	}

	// 2. 回声模式:Up 且生效时驱动回声报文与回声检测
	if s.echoModeActive() {
		s.maybeSendEcho(now)
	}

	// 3. 控制报文发送(含按需模式静默规则)
	s.maybeSendControl(now)

	// 4. 重新布防下一次唤醒
	s.armNextWake(now)
}

// effectiveControlDeadline 计算控制面实际检测死线 = 收包时刻 + 检测时间 + 宽限。
func (s *Session) effectiveControlDeadline(now int64) int64 {
	return now + s.DetectionTime() + s.cfg.DetectionGrace
}

// effectiveEchoDeadline 计算回声实际检测死线 = 当前时刻 + 回声检测时间 + 宽限。
func (s *Session) effectiveEchoDeadline(now int64) int64 {
	return now + s.EchoDetectionTime() + s.cfg.DetectionGrace
}

// checkTimeouts 检查控制面与回声两条检测死线。检测死线仅在会话 Up 后布防,
// Down/Init 建链阶段不做超时判定,避免慢定时器建链期误判。
func (s *Session) checkTimeouts(now int64) {
	if s.state != StateUp {
		return
	}
	// 回声检测优先:回声模式生效时由回声流承担连通性判定
	if s.echoModeActive() && s.echoDeadline != 0 && now >= s.echoDeadline {
		s.enterDown(DiagEchoFailed, now, "回声检测时间超时")
		return
	}
	// 控制面检测:回声模式由回声流承担。按需模式仅在主动轮询确认(pollActive)
	// 期间启用死线判定;静默期不判定,等待下一次主动轮询。
	if !s.echoModeActive() &&
		s.controlDeadline != 0 && now >= s.controlDeadline {
		if s.demandModeActive() && !s.pollActive {
			// 静默期死线到期:不判 Down,撤销死线等待下次轮询
			s.controlDeadline = 0
			return
		}
		s.enterDown(DiagControlDetectExp, now, "控制检测时间超时")
		return
	}
}

// due 判断距上次发包是否已达到 gap 间隔(时间门控,使多余唤醒不会过发)。
func (s *Session) due(now, gap int64) bool {
	return s.lastPacketAt == 0 || now-s.lastPacketAt >= gap
}

// maybeSendControl 依据当前状态与模式决定是否发送控制报文。
func (s *Session) maybeSendControl(now int64) {
	// 按需模式生效后平时不发任何异步报文;仅在主动轮询确认期间重发 P 位报文
	if s.demandModeActive() {
		if s.pollActive && s.due(now, s.txInterval) {
			s.transmitControl(now, true)
		}
		return
	}
	// 回声模式下连通性由回声流承担,异步控制报文降到很低频率(1 秒一个)
	if s.echoModeActive() {
		slow := int64(DemandSlowRate) * int64(time.Second)
		if s.due(now, slow) {
			s.transmitControl(now, false)
		}
		return
	}
	// 轮询序列进行中:按当前(旧)协商间隔重发带 P 位的报文
	if s.pollActive {
		if s.due(now, s.txInterval) {
			s.transmitControl(now, true)
		}
		return
	}
	// 普通异步模式:Up 后按协商间隔发送;Down/Init 用本地期望间隔快速建链
	gap := s.txInterval
	if s.state != StateUp {
		gap = s.cfg.DesiredMinTx
	}
	if s.due(now, gap) {
		s.transmitControl(now, false)
	}
}

// maybeSendEcho 按协商回声间隔发送回声报文。
func (s *Session) maybeSendEcho(now int64) {
	interval := s.echoInterval
	if interval <= 0 {
		return
	}
	if s.lastEchoAt == 0 || now-s.lastEchoAt >= interval {
		s.echoSeq++
		echo := &EchoPacket{
			Magic: EchoMagic, Sender: s.cfg.LocalDisc,
			Seq: s.echoSeq, SendTime: now,
		}
		s.txEchoPackets++
		s.lastEchoAt = now
		s.sendEcho(echo)
		s.emit(EvSendEcho, "seq="+itoa(int64(echo.Seq)))
	}
}

// armNextWake 计算下一个最近事件时刻并向引擎预约。唤醒取"下一次发送时刻"
// 与"检测死线"的最近值,保证死线到期时一定有一次唤醒执行判定。
// 只纳入严格晚于当前时刻 now 的事件,避免对过去时刻反复排程导致空转。
func (s *Session) armNextWake(now int64) {
	next := int64(-1)
	add := func(t int64) {
		if t > now && (next < 0 || t < next) {
			next = t
		}
	}
	// 下一个控制发送时刻 = 上次发包 + 当前发送间隔
	add(s.lastPacketAt + s.currentSendGap(now))
	// 回声发送时刻
	if s.echoModeActive() && s.echoInterval > 0 {
		base := s.lastEchoAt
		if base == 0 {
			base = now
		}
		add(base + s.echoInterval)
	}
	// 检测死线(到期必须唤醒以执行判定)
	if s.controlDeadline != 0 {
		add(s.controlDeadline)
	}
	if s.echoModeActive() && s.echoDeadline != 0 {
		add(s.echoDeadline)
	}
	if next > 0 {
		s.armTimer(next - now)
	}
}

// currentSendGap 返回当前状态下的控制发送间隔。
func (s *Session) currentSendGap(now int64) int64 {
	slow := int64(DemandSlowRate) * int64(time.Second)
	// Down/Init 建链阶段:以本地期望间隔快速重发,保证丢包下也能快速完成
	// 三次握手(若严格采用 RFC 的 1s 慢速定时器,高丢包时建链会显著变慢)。
	if s.state != StateUp {
		return s.cfg.DesiredMinTx
	}
	// 按需模式生效后无周期发送;轮询确认期间按协商间隔重发 P 位报文
	if s.demandModeActive() {
		if s.pollActive {
			return s.txInterval
		}
		return slow
	}
	// 回声模式下异步控制报文降到很低频率
	if s.echoModeActive() {
		return slow
	}
	_ = now
	return s.txInterval
}

// ---- 报文构造与发送 ----

// transmitControl 构造并发送一个控制报文,poll 强制置 P 位。
func (s *Session) transmitControl(now int64, poll bool) {
	yourDisc := uint32(0)
	if s.remoteKnown {
		yourDisc = s.remoteDisc
	}
	// 轮询序列未完成前,报文继续携带旧的协商参数
	tx := s.txInterval
	rx := s.cfg.RequiredMinRx
	echo := s.cfg.RequiredMinEcho
	mult := s.cfg.DetectMult
	if s.hasPending && !poll {
		// 非 P 位普通报文在确认前同样沿用旧参数
	}
	p := &ControlPacket{
		Version: Version, Diag: s.diag, State: s.state,
		Poll: poll, Final: false, ControlPlane: true,
		Demand:     s.cfg.DemandEnabled,
		DetectMult: mult, MyDisc: s.cfg.LocalDisc, YourDisc: yourDisc,
		DesiredMinTx: tx, RequiredMinRx: rx, RequiredMinEcho: echo,
	}
	s.txPackets++
	s.lastPacketAt = now
	s.sendCtrl(p)
	detail := "state=" + p.State.String() + " mult=" + itoa(int64(mult)) +
		" txMs=" + ftoaMs(tx) + " rxMs=" + ftoaMs(rx)
	if poll {
		detail = "P 位轮询 " + detail
		s.emit(EvPoll, detail)
	} else {
		s.emit(EvSendControl, detail)
	}
}

// sendFinal 单独发送一个 F 位应答(参数镜像当前配置)。
func (s *Session) sendFinal(now int64) {
	yourDisc := uint32(0)
	if s.remoteKnown {
		yourDisc = s.remoteDisc
	}
	p := &ControlPacket{
		Version: Version, Diag: s.diag, State: s.state,
		Poll: false, Final: true, ControlPlane: true,
		Demand:     s.cfg.DemandEnabled,
		DetectMult: s.cfg.DetectMult, MyDisc: s.cfg.LocalDisc, YourDisc: yourDisc,
		DesiredMinTx: s.txInterval, RequiredMinRx: s.cfg.RequiredMinRx,
		RequiredMinEcho: s.cfg.RequiredMinEcho,
	}
	s.txPackets++
	s.lastPacketAt = now
	s.sendCtrl(p)
	s.emit(EvFinal, "应答对端 P 位")
}

// ---- 报文接收与状态机 ----

// HandleControl 处理一个已通过链路到达、尚未做协议校验的控制报文字节。
// 返回丢弃原因(空串表示接受)。
func (s *Session) HandleControl(raw []byte, now int64) DropReason {
	if s.closed {
		return ""
	}
	p, reason := ParseControl(raw)
	if reason != "" {
		s.dropCounts[reason]++
		s.emit(EvDrop, string(reason))
		return reason
	}

	oldRemoteKnown := s.remoteKnown
	s.rxPackets++
	s.lastPacketAt = now
	s.lastRecvAt = now

	// 学习/刷新对端参数
	s.remoteKnown = true
	s.remoteState = p.State
	s.remoteDisc = p.MyDisc
	s.remoteMult = p.DetectMult
	s.remoteDesiredTx = p.DesiredMinTx
	s.remoteRequiredRx = p.RequiredMinRx
	s.remoteEchoRx = p.RequiredMinEcho
	s.remoteDemand = p.Demand

	// 重新协商定时器(新收包间隔立即用于检测;本端发送间隔在
	// 非参数变更场景下随对端要求即时更新)
	s.refreshIntervals(oldRemoteKnown)

	// 处理 F 位:对端确认了本端的轮询序列,切换到新参数
	if p.Final {
		if s.pollActive {
			wasDemandVerify := s.demandModeActive() && !s.hasPending
			s.pollActive = false
			if s.hasPending {
				s.applyPending(now)
			}
			if wasDemandVerify {
				// 按需确认成功:连通性已验证。把检测死线刷新为"当前时刻+检测
				// 时间+宽限",覆盖到下一次主动轮询;若期间链路中断,死线到期
				// 即判定 Down,使按需模式同样能检测故障。
				if s.DetectionTime() > 0 {
					s.controlDeadline = s.effectiveControlDeadline(now)
				}
			}
		} else {
			s.emit(EvFinal, "收到非预期 F 位,仅记录")
		}
	}

	// 处理 P 位:必须尽快回应 F 位(RFC 5880 §6.8.7)
	if p.Poll {
		s.sendFinal(now)
	}

	s.emit(EvRecvControl, "remote="+p.State.String()+
		" mult="+itoa(int64(p.DetectMult))+
		" txMs="+ftoaMs(p.DesiredMinTx)+" rxMs="+ftoaMs(p.RequiredMinRx)+
		flagStr(p.Poll, p.Final, p.Demand))

	// 状态机转换(RFC 5880 §6.8.6 状态转换表)
	s.transition(p, now)

	// 收到合法报文即刷新检测死线(仅 Up 态):回声模式由回声流布防;
	// 异步与按需模式均按"检测时间+宽限"布防控制死线。刷新后重新排程,
	// 代次机制使旧定时器作废,保证唤醒始终对齐最新死线,杜绝幽灵定时器。
	if s.state == StateUp {
		if s.echoModeActive() {
			s.echoDeadline = s.effectiveEchoDeadline(now)
		} else {
			s.armControlDeadline(now)
		}
		s.armNextWake(now)
	}
	return ""
}

// HandleEcho 处理到达的回声报文。对端回送的报文回到本端时调用。
func (s *Session) HandleEcho(raw []byte, now int64) DropReason {
	if s.closed {
		return ""
	}
	e, reason := ParseEcho(raw)
	if reason != "" {
		s.dropCounts[reason]++
		s.emit(EvDrop, string(reason))
		return reason
	}
	s.rxEchoPackets++
	s.lastEchoRecvAt = now
	// 收到回声即刷新回声检测死线并重新排程(代次机制作废旧定时器)
	if s.echoModeActive() {
		s.echoDeadline = s.effectiveEchoDeadline(now)
		s.armNextWake(now)
	}
	s.emit(EvRecvEcho, "seq="+itoa(int64(e.Seq))+" rttMs="+ftoaMs(now-e.SendTime))
	return ""
}

// transition 按 RFC 5880 标准三态转换表推进状态。
func (s *Session) transition(p *ControlPacket, now int64) {
	switch s.state {
	case StateDown:
		// Down:收到 Down/AdminDown/Init -> Init;收到 Up 保持 Down
		if p.State == StateDown || p.State == StateAdminDown || p.State == StateInit {
			s.changeState(StateInit, now, "收到对端 "+p.State.String())
		}
	case StateInit:
		// Init:收到 Init/Up -> Up;收到 Down/AdminDown -> Down
		if p.State == StateInit || p.State == StateUp {
			s.changeState(StateUp, now, "收到对端 "+p.State.String())
		} else if p.State == StateDown || p.State == StateAdminDown {
			s.enterDown(DiagNeighborSignaled, now, "对端通知 "+p.State.String())
		}
	case StateUp:
		// Up:收到 Down/AdminDown -> Down;Init/Up 保持 Up
		if p.State == StateDown || p.State == StateAdminDown {
			s.enterDown(DiagNeighborSignaled, now, "对端通知 "+p.State.String())
		}
	case StateAdminDown:
		// 管理关闭态不参与转换
	}
}

// changeState 是 Up 方向的统一状态切换入口,负责通知上层。
func (s *Session) changeState(new State, now int64, why string) {
	if s.state == new {
		return
	}
	old := s.state
	s.state = new
	if new == StateUp {
		s.diag = DiagNone
	}
	s.emit(EvStateChange, old.String()+" -> "+new.String()+" ("+why+")")
	if s.notify != nil {
		s.notify(old, new, s.diag)
	}
	// 进入 Up 后立即按新协商结果布防检测死线并重新排程
	if new == StateUp {
		s.refreshIntervals(false)
		if !s.echoModeActive() {
			s.armControlDeadline(now)
		} else {
			s.echoDeadline = s.effectiveEchoDeadline(now)
		}
		s.armNextWake(now)
	}
}

// enterDown 是 Down 方向的统一入口(超时或对端通知),复位序列状态。
func (s *Session) enterDown(diag Diag, now int64, why string) {
	old := s.state
	s.diag = diag
	s.state = StateDown
	s.downCount++
	s.pollActive = false
	s.hasPending = false
	s.controlDeadline = 0
	s.echoDeadline = 0
	s.emit(EvTimeout, why)
	s.emit(EvStateChange, old.String()+" -> Down ("+why+")")
	if s.notify != nil {
		s.notify(old, StateDown, diag)
	}
}

// armControlDeadline 按 检测时间=对端检测倍数×协商收包间隔(+宽限)布防。
func (s *Session) armControlDeadline(now int64) {
	if s.DetectionTime() > 0 {
		s.controlDeadline = s.effectiveControlDeadline(now)
	}
}

// refreshIntervals 根据最新对端参数重算协商间隔。
func (s *Session) refreshIntervals(firstLearn bool) {
	newTx := s.effectiveTxInterval()
	newRx := s.effectiveRxInterval()
	newEcho := s.effectiveEchoInterval()
	if firstLearn || !s.hasPending {
		s.txInterval = newTx
	}
	s.rxInterval = newRx
	s.echoInterval = newEcho
}

// ---- 参数变更:轮询/终止序列(需求 4) ----

// RequestParams 发起参数变更。本端进入轮询序列:在收到对端 F 位确认前,
// 所有报文继续携带并使用旧间隔与旧检测倍数;确认到达后一次性切换到新参数。
// 新参数暂存在 pending* 字段,cfg 本身在确认前保持不变。
func (s *Session) RequestParams(desiredTx, requiredRx, requiredEcho int64, mult uint8, now int64) {
	if !s.remoteKnown {
		return
	}
	// 新参数只暂存,cfg 在收到 F 确认前保持不变,报文继续携带旧参数
	s.pendingDesiredTx = desiredTx
	s.pendingRequiredRx = requiredRx
	s.pendingRequiredEcho = requiredEcho
	s.pendingMult = mult
	s.hasPending = true
	s.pollActive = true
	s.pollSeq++
	s.emit(EvParamChange, "请求变更参数,等待对端 F 确认;确认前沿用旧间隔")
	// 立即发送一个带 P 位的报文(携带旧参数)
	s.transmitControl(now, true)
}

// applyPending 在收到对端 F 位后调用,正式启用新参数并重算协商结果。
func (s *Session) applyPending(now int64) {
	if !s.hasPending {
		return
	}
	s.hasPending = false
	s.cfg.DesiredMinTx = s.pendingDesiredTx
	s.cfg.RequiredMinRx = s.pendingRequiredRx
	s.cfg.RequiredMinEcho = s.pendingRequiredEcho
	s.cfg.DetectMult = s.pendingMult
	s.txInterval = s.effectiveTxInterval()
	s.rxInterval = s.effectiveRxInterval()
	s.echoInterval = s.effectiveEchoInterval()
	s.emit(EvParamChange, "收到 F 确认,启用新间隔 txMs="+ftoaMs(s.txInterval)+
		" 检测时间Ms="+ftoaMs(int64(s.remoteMult)*s.rxInterval))
	if s.state == StateUp {
		if !s.echoModeActive() {
			s.armControlDeadline(now)
		} else {
			s.echoDeadline = s.effectiveEchoDeadline(now)
		}
	}
}

// ---- 按需模式:主动轮询确认(需求 6) ----

// DemandVerify 在按需模式下主动发起一次连通性确认:发送 P 位报文并布防
// 检测死线;对端 F 位(或任何合法报文)返回即视为连通。
// 这是按需模式唯一的外部唤醒入口(静默期无周期定时器),故此处主动排程。
func (s *Session) DemandVerify(now int64) {
	if s.state != StateUp {
		return
	}
	s.pollActive = true
	s.transmitControl(now, true)
	if s.DetectionTime() > 0 {
		s.controlDeadline = s.effectiveControlDeadline(now)
	}
	s.armNextWake(now)
}

// MarkFaultTime 由场景层标注最近一次故障注入时刻,用于检测时延统计。
func (s *Session) MarkFaultTime(now int64) { s.pendingFaultAt = now }

// Close 终止会话。
func (s *Session) Close() { s.closed = true }

// ---- 只读快照(供 Web/统计) ----

// Snapshot 是会话状态的 JSON 友好快照。
type Snapshot struct {
	Node              string           `json:"node"`
	LinkID            string           `json:"linkId"`
	State             string           `json:"state"`
	Diag              string           `json:"diag"`
	LocalDisc         uint32           `json:"localDisc"`
	RemoteDisc        uint32           `json:"remoteDisc"`
	RemoteState       string           `json:"remoteState"`
	RemoteKnown       bool             `json:"remoteKnown"`
	LocalDesiredTx    int64            `json:"localDesiredTxNs"`
	LocalRequiredRx   int64            `json:"localRequiredRxNs"`
	LocalRequiredEcho int64            `json:"localRequiredEchoNs"`
	DetectMult        uint8            `json:"detectMult"`
	RemoteMult        uint8            `json:"remoteMult"`
	RemoteDesiredTx   int64            `json:"remoteDesiredTxNs"`
	RemoteRequiredRx  int64            `json:"remoteRequiredRxNs"`
	TxInterval        int64            `json:"txIntervalNs"`
	RxInterval        int64            `json:"rxIntervalNs"`
	EchoInterval      int64            `json:"echoIntervalNs"`
	DetectionTime     int64            `json:"detectionTimeNs"`
	EchoDetectionTime int64            `json:"echoDetectionTimeNs"`
	EchoEnabled       bool             `json:"echoEnabled"`
	DemandEnabled     bool             `json:"demandEnabled"`
	EchoActive        bool             `json:"echoActive"`
	DemandActive      bool             `json:"demandActive"`
	PollActive        bool             `json:"pollActive"`
	HasPending        bool             `json:"hasPending"`
	RxPackets         int64            `json:"rxPackets"`
	TxPackets         int64            `json:"txPackets"`
	RxEchoPackets     int64            `json:"rxEchoPackets"`
	TxEchoPackets     int64            `json:"txEchoPackets"`
	DownCount         int64            `json:"downCount"`
	DropCounts        map[string]int64 `json:"dropCounts"`
	ControlDeadline   int64            `json:"controlDeadlineNs"`
	EchoDeadline      int64            `json:"echoDeadlineNs"`
}

// Snapshot 采集会话快照。
func (s *Session) Snapshot() Snapshot {
	drop := map[string]int64{}
	for k, v := range s.dropCounts {
		drop[string(k)] = v
	}
	remoteState := "未知"
	if s.remoteKnown {
		remoteState = s.remoteState.String()
	}
	return Snapshot{
		Node: s.cfg.Node, LinkID: s.cfg.LinkID,
		State: s.state.String(), Diag: s.diag.String(),
		LocalDisc: s.cfg.LocalDisc, RemoteDisc: s.remoteDisc,
		RemoteState: remoteState, RemoteKnown: s.remoteKnown,
		LocalDesiredTx: s.cfg.DesiredMinTx, LocalRequiredRx: s.cfg.RequiredMinRx,
		LocalRequiredEcho: s.cfg.RequiredMinEcho,
		DetectMult:        s.cfg.DetectMult, RemoteMult: s.remoteMult,
		RemoteDesiredTx: s.remoteDesiredTx, RemoteRequiredRx: s.remoteRequiredRx,
		TxInterval: s.txInterval, RxInterval: s.rxInterval, EchoInterval: s.echoInterval,
		DetectionTime: s.DetectionTime(), EchoDetectionTime: s.EchoDetectionTime(),
		EchoEnabled: s.cfg.EchoEnabled, DemandEnabled: s.cfg.DemandEnabled,
		EchoActive: s.echoModeActive(), DemandActive: s.demandModeActive(),
		PollActive: s.pollActive, HasPending: s.hasPending,
		RxPackets: s.rxPackets, TxPackets: s.txPackets,
		RxEchoPackets: s.rxEchoPackets, TxEchoPackets: s.txEchoPackets,
		DownCount: s.downCount, DropCounts: drop,
		ControlDeadline: s.controlDeadline, EchoDeadline: s.echoDeadline,
	}
}

// ---- 小工具 ----

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func ftoaMs(ns int64) string {
	ms := ns / 1_000_000
	return itoa(ms)
}

func flagStr(poll, final, demand bool) string {
	out := ""
	if poll {
		out += " [P]"
	}
	if final {
		out += " [F]"
	}
	if demand {
		out += " [D]"
	}
	return out
}

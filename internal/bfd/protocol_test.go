package bfd

import "testing"

// TestPacketValidation 验证报文字段合法性检查(需求 8)。校验作用于线上字节,
// 而 Encode 总会写出合法字段,因此对"字段非法"类用例先编码再篡改对应字节。
func TestPacketValidation(t *testing.T) {
	base := func() *ControlPacket {
		return &ControlPacket{
			Version: Version, State: StateUp, Diag: DiagNone,
			DetectMult: 3, Length: MinHdrLen, MyDisc: 1, YourDisc: 2,
			DesiredMinTx: 1e8, RequiredMinRx: 1e8,
		}
	}
	// 先得到一份合法线上字节,再逐位篡改
	validRaw := base().Encode()
	cases := []struct {
		name   string
		mutate func([]byte)
		want   DropReason
	}{
		{"版本错误", func(b []byte) { b[0] = (b[0] & 0x1f) | (2 << 5) }, DropBadVersion},
		{"认证位置位", func(b []byte) { b[1] |= 1 << 2 }, DropAuthPresent},
		{"多点位置位", func(b []byte) { b[1] |= 1 }, DropMultipointSet},
		{"P/F同置", func(b []byte) { b[1] |= (1 << 5) | (1 << 4) }, DropPollFinalBoth},
		{"Up态YourDisc为0", func(b []byte) { putU32(b[8:12], 0) }, DropZeroYourDisc},
		{"MyDisc为0", func(b []byte) { putU32(b[4:8], 0) }, DropZeroMyDisc},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := make([]byte, len(validRaw))
			copy(raw, validRaw)
			c.mutate(raw)
			if _, reason := ParseControl(raw); reason != c.want {
				t.Fatalf("期望 %s,实际 %s", c.want, reason)
			}
		})
	}
	// 合法报文通过
	if _, reason := ParseControl(validRaw); reason != "" {
		t.Fatalf("合法报文被拒: %s", reason)
	}
	// 长度不足通过截断的原始字节触发
	if _, reason := ParseControl(validRaw[:10]); reason != DropTooShort {
		t.Fatalf("截断报文期望 %s,实际 %s", DropTooShort, reason)
	}
}

// TestStateMachine 验证三态转换表(需求 1)。
func TestStateMachine(t *testing.T) {
	newSess := func() *Session {
		return NewSession(Config{LocalDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8, DetectMult: 3},
			func(after int64) uint64 { return 0 },
			func(p *ControlPacket) {}, func(e *EchoPacket) {},
			func(evType, detail string) {}, func(old, new State, diag Diag) {})
	}
	upPkt := func(state State) *ControlPacket {
		return &ControlPacket{Version: Version, State: state, DetectMult: 3,
			MyDisc: 2, YourDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8}
	}
	t.Run("Down收Init转Init", func(t *testing.T) {
		s := newSess()
		s.HandleControl(upPkt(StateInit).Encode(), 0)
		if s.State() != StateInit {
			t.Fatalf("got %s", s.State())
		}
	})
	t.Run("Init收Up转Up", func(t *testing.T) {
		s := newSess()
		s.HandleControl(upPkt(StateInit).Encode(), 0)
		s.HandleControl(upPkt(StateUp).Encode(), 1e8)
		if s.State() != StateUp {
			t.Fatalf("got %s", s.State())
		}
	})
	t.Run("Up收Down转Down", func(t *testing.T) {
		s := newSess()
		s.HandleControl(upPkt(StateInit).Encode(), 0)
		s.HandleControl(upPkt(StateUp).Encode(), 1e8)
		s.HandleControl(upPkt(StateDown).Encode(), 2e8)
		if s.State() != StateDown {
			t.Fatalf("got %s", s.State())
		}
	})
}

// TestNegotiation 验证收发间隔取较大值、检测时间公式(需求 2、3)。
func TestNegotiation(t *testing.T) {
	// 时间单位为纳秒:100ms=1e8。报文字段按微秒序列化,需用微秒级整数。
	const I100 int64 = 1e8
	const I150 int64 = 150 * 1e6
	const I200 int64 = 2e8
	const I250 int64 = 250 * 1e6
	s := NewSession(Config{LocalDisc: 1, DesiredMinTx: I100, RequiredMinRx: I200, DetectMult: 3},
		func(after int64) uint64 { return 0 },
		func(p *ControlPacket) {}, func(e *EchoPacket) {},
		func(evType, detail string) {}, func(old, new State, diag Diag) {})
	p := &ControlPacket{Version: Version, State: StateInit, DetectMult: 4,
		MyDisc: 2, YourDisc: 1, DesiredMinTx: I150, RequiredMinRx: I250}
	s.HandleControl(p.Encode(), 0)
	// 发包间隔 = max(本端DesiredMinTx=100ms, 对端RequiredMinRx=250ms) = 250ms
	if s.txInterval != I250 {
		t.Fatalf("txInterval=%d 期望%d", s.txInterval, I250)
	}
	// 收包间隔 = max(对端DesiredMinTx=150ms, 本端RequiredMinRx=200ms) = 200ms
	if s.RxInterval() != I200 {
		t.Fatalf("rxInterval=%d 期望%d", s.RxInterval(), I200)
	}
	// 检测时间 = 对端检测倍数4 × 收包间隔200ms = 800ms
	if s.DetectionTime() != 4*I200 {
		t.Fatalf("DetectionTime=%d 期望%d", s.DetectionTime(), 4*I200)
	}
}

// TestEchoPacketValidation 验证回声报文校验。
func TestEchoPacketValidation(t *testing.T) {
	valid := &EchoPacket{Magic: EchoMagic, Sender: 1, Seq: 1, SendTime: 100}
	if _, reason := ParseEcho(valid.Encode()); reason != "" {
		t.Fatalf("合法回声被拒: %s", reason)
	}
	bad := &EchoPacket{Magic: 0, Sender: 1, Seq: 1, SendTime: 100}
	if _, reason := ParseEcho(bad.Encode()); reason != DropEchoMagic {
		t.Fatalf("期望 %s 实际 %s", DropEchoMagic, reason)
	}
}

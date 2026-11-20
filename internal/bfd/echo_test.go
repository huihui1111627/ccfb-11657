package bfd

import "testing"

// TestEchoMode 验证回声模式:回声检测生效、异步控制报文降为低频(需求 5)。
func TestEchoMode(t *testing.T) {
	var ctrl, echo int
	s := NewSession(Config{LocalDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8,
		RequiredMinEcho: 1e8, DetectMult: 3, EchoEnabled: true},
		func(after int64) uint64 { return 0 },
		func(p *ControlPacket) { ctrl++ },
		func(e *EchoPacket) { echo++ },
		func(evType, detail string) {}, func(old, new State, diag Diag) {})
	// 对端也支持回声(RequiredMinEcho>0)
	p := func(state State) *ControlPacket {
		return &ControlPacket{Version: Version, State: state, DetectMult: 3,
			MyDisc: 2, YourDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8,
			RequiredMinEcho: 1e8}
	}
	s.HandleControl(p(StateInit).Encode(), 0)
	s.HandleControl(p(StateUp).Encode(), 1e8)
	if !s.echoModeActive() {
		t.Fatal("两端支持回声且 Up 后应进入回声模式")
	}
	if s.EchoDetectionTime() != 3*1e8 {
		t.Fatalf("回声检测时间=%d,期望 3×间隔", s.EchoDetectionTime())
	}
	// 在 Up 态驱动若干次唤醒:回声按 100ms 间隔发送,控制报文降为 1s 低频。
	// 每次唤醒后回送一个回声,刷新回声检测死线以保持 Up。
	now := int64(1e8)
	echoReply := &EchoPacket{Magic: EchoMagic, Sender: 1, Seq: 1, SendTime: now}
	for i := 0; i < 12; i++ {
		now += 1e8 // 每次推进 100ms
		s.Wake(now)
		echoReply.SendTime = now
		s.HandleEcho(echoReply.Encode(), now+4e6)
	}
	if s.State() != StateUp {
		t.Fatalf("回声正常回送下应保持 Up,实际 %s", s.State())
	}
	if echo < 10 {
		t.Fatalf("回声报文发送次数=%d,期望约 10+", echo)
	}
	// 1.2s 内控制报文应很少(降频到 1s,至多 2 个)
	if ctrl > 3 {
		t.Fatalf("回声模式下控制报文未降频: %d 个", ctrl)
	}
}

// TestEchoDetectionTimeout 验证回声流中断时按回声检测时间判 Down。
func TestEchoDetectionTimeout(t *testing.T) {
	s, _ := upSessionWithCapture()
	// 切换为回声配置
	s.cfg.EchoEnabled = true
	s.cfg.RequiredMinEcho = 1e8
	s.remoteEchoRx = 1e8
	s.echoInterval = 1e8
	s.state = StateUp
	// 布防回声死线后不回送任何回声,推进超过检测时间
	s.echoDeadline = s.effectiveEchoDeadline(1e8)
	s.Wake(1e8 + 3*1e8 + 1)
	if s.State() != StateDown {
		t.Fatalf("回声超时后应 Down,实际 %s", s.State())
	}
}

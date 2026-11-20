package bfd

import "testing"

// 构造一个已 Up、能观察发送报文的会话。
func upSessionWithCapture() (*Session, *[]*ControlPacket) {
	var sent []*ControlPacket
	s := NewSession(Config{LocalDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8, DetectMult: 3},
		func(after int64) uint64 { return 0 },
		func(p *ControlPacket) { sent = append(sent, p) },
		func(e *EchoPacket) {},
		func(evType, detail string) {}, func(old, new State, diag Diag) {})
	up := func(state State) *ControlPacket {
		return &ControlPacket{Version: Version, State: state, DetectMult: 3,
			MyDisc: 2, YourDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8}
	}
	s.HandleControl(up(StateInit).Encode(), 0)
	s.HandleControl(up(StateUp).Encode(), 1e8)
	return s, &sent
}

// TestPollFinalSequence 验证参数变更走 P/F 轮询,确认前沿用旧间隔(需求 4)。
func TestPollFinalSequence(t *testing.T) {
	s, sent := upSessionWithCapture()
	oldTx := s.cfg.DesiredMinTx
	// 请求把间隔从 100ms 改为 200ms
	s.RequestParams(2e8, 2e8, 0, 3, 2e8)
	if !s.pollActive || !s.hasPending {
		t.Fatal("请求后应进入轮询且有待确认参数")
	}
	// 确认前 cfg 不变
	if s.cfg.DesiredMinTx != oldTx {
		t.Fatalf("确认前参数已变更: %d", s.cfg.DesiredMinTx)
	}
	// 应已发出带 P 位的报文
	last := (*sent)[len(*sent)-1]
	if !last.Poll || last.Final {
		t.Fatal("参数请求应发送 P=1,F=0 的报文")
	}
	// 对端回 F 位确认
	fPkt := &ControlPacket{Version: Version, State: StateUp, DetectMult: 3,
		MyDisc: 2, YourDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8, Final: true}
	s.HandleControl(fPkt.Encode(), 3e8)
	if s.hasPending || s.pollActive {
		t.Fatal("收到 F 后应结束轮询并应用参数")
	}
	if s.cfg.DesiredMinTx != 2e8 {
		t.Fatalf("确认后参数未生效: %d", s.cfg.DesiredMinTx)
	}
}

// TestDemandSilence 验证按需模式平时静默、主动轮询时才发报文(需求 6)。
func TestDemandSilence(t *testing.T) {
	var sent []*ControlPacket
	s := NewSession(Config{LocalDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8,
		DetectMult: 3, DemandEnabled: true},
		func(after int64) uint64 { return 0 },
		func(p *ControlPacket) { sent = append(sent, p) },
		func(e *EchoPacket) {},
		func(evType, detail string) {}, func(old, new State, diag Diag) {})
	// 对端也置 D 位,使按需模式生效
	dp := func(state State) *ControlPacket {
		p := &ControlPacket{Version: Version, State: state, DetectMult: 3,
			MyDisc: 2, YourDisc: 1, DesiredMinTx: 1e8, RequiredMinRx: 1e8, Demand: true}
		return p
	}
	s.HandleControl(dp(StateInit).Encode(), 0)
	s.HandleControl(dp(StateUp).Encode(), 1e8)
	if !s.demandModeActive() {
		t.Fatal("两端 D=1 应进入按需模式")
	}
	before := len(sent)
	// 普通唤醒(非轮询)在按需静默期不应发控制报文
	s.maybeSendControl(5e8)
	if len(sent) != before {
		t.Fatal("按需静默期普通唤醒不应发送报文")
	}
	// 主动发起轮询确认时应发 P 位报文
	s.DemandVerify(6e8)
	if !s.pollActive {
		t.Fatal("DemandVerify 应进入轮询态")
	}
	last := sent[len(sent)-1]
	if !last.Poll {
		t.Fatal("主动轮询应发送 P 位报文")
	}
}

package sim

import "testing"

// TestLinkCut 验证单向与双向中断只丢弃对应方向的报文。
func TestLinkCut(t *testing.T) {
	eng := NewEngine(1, nil)
	topo := NewTopology(eng)
	eng.handler = topo
	l := NewLink(eng, LinkConfig{ID: "x", BaseDelay: ms(2)})
	topo.AddLink(l)
	env := func(target uint32) Envelope {
		return Envelope{Kind: EvControl, Target: target, LinkID: "x"}
	}
	// 单向中断 A->B
	l.SetCut(DirAtoB, true)
	l.Transmit(0, env(2)) // A->B 被丢
	l.Transmit(1, env(1)) // B->A 通过
	st := l.Snapshot()
	if st.CutDroppedAtoB != 1 || st.DeliveredBtoA != 1 {
		t.Fatalf("单向中断方向错误: %+v", st)
	}
	// 双向中断
	l.SetCut(DirBtoA, true)
	l.Transmit(0, env(2))
	l.Transmit(1, env(1))
	st = l.Snapshot()
	if st.CutDroppedAtoB != 2 || st.CutDroppedBtoA != 1 {
		t.Fatalf("双向中断计数错误: %+v", st)
	}
}

// TestLinkLoss 验证丢包率作用且中断优先于丢包。
func TestLinkLoss(t *testing.T) {
	eng := NewEngine(3, nil)
	topo := NewTopology(eng)
	eng.handler = topo
	l := NewLink(eng, LinkConfig{ID: "x", BaseDelay: ms(1), LossPct: 0.5})
	topo.AddLink(l)
	env := Envelope{Kind: EvControl, Target: 2, LinkID: "x"}
	for i := 0; i < 100; i++ {
		l.Transmit(0, env)
	}
	st := l.Snapshot()
	if st.LostAtoB == 0 || st.LostAtoB >= 100 {
		t.Fatalf("50%% 丢包下丢失数异常: %d", st.LostAtoB)
	}
	if st.LostAtoB+st.DeliveredAtoB != 100 {
		t.Fatal("发送总数应等于丢失+送达")
	}
}

// TestDeterministic 验证同种子两次运行的链路计数完全一致(可复现)。
func TestDeterministic(t *testing.T) {
	run := func() Stats {
		eng := NewEngine(123, nil)
		topo := NewTopology(eng)
		eng.handler = topo
		l := NewLink(eng, LinkConfig{ID: "x", BaseDelay: ms(2), Jitter: ms(5), LossPct: 0.2})
		topo.AddLink(l)
		env := Envelope{Kind: EvControl, Target: 2, LinkID: "x"}
		for i := 0; i < 200; i++ {
			l.Transmit(0, env)
			l.Transmit(1, env)
		}
		return l.Snapshot()
	}
	a, b := run(), run()
	if a != b {
		t.Fatalf("同种子结果不一致:\n%+v\n%+v", a, b)
	}
}

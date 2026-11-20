package sim

import "testing"

// TestUpperLayerSwitchover 验证会话 Up->Down 时通知上层,切换器把流量切到备用链路(需求 7)。
func TestUpperLayerSwitchover(t *testing.T) {
	eng := NewEngine(42, nil)
	topo := NewTopology(eng)
	eng.handler = topo
	pri := NewLink(eng, LinkConfig{ID: "primary", BaseDelay: ms(2), Jitter: 0})
	bak := NewLink(eng, LinkConfig{ID: "backup", BaseDelay: ms(2), Jitter: 0})
	topo.AddLink(pri)
	topo.AddLink(bak)
	cfg := EndpointCfg{DesiredMinTx: ms(100), RequiredMinRx: ms(100), DetectMult: 3}
	topo.AddSessionPair("primary", cfg, cfg)
	topo.AddSessionPair("backup", cfg, cfg)
	topo.Start()

	swA, swB := topo.Switches()
	if swA.ActiveLink() != "primary" || swB.ActiveLink() != "primary" {
		t.Fatal("初始流量应走主用链路")
	}
	// 稳态建立后双向中断主链路
	eng.Schedule(ms(1500), EvAction, 0, func(now int64) {
		pri.CutBoth(true)
	})
	eng.StopAt(ms(4000))
	eng.Run()

	// 两端都应检测到断开并切换到备用链路
	if swA.ActiveLink() != "backup" {
		t.Fatalf("山顶未切到备用链路,当前=%s", swA.ActiveLink())
	}
	if swB.ActiveLink() != "backup" {
		t.Fatalf("山脚未切到备用链路,当前=%s", swB.ActiveLink())
	}
	if len(swA.Events()) == 0 || len(swB.Events()) == 0 {
		t.Fatal("应记录切换事件")
	}
	// 主链路应观测到中断丢包
	st := pri.Snapshot()
	if st.CutDroppedAtoB == 0 && st.CutDroppedBtoA == 0 {
		t.Fatal("中断后应有被丢弃的报文")
	}
}

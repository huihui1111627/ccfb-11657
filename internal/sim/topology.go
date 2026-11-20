package sim

import (
	"bfdsim/internal/bfd"
)

// TrafficSwitch 模拟一个端点上的上层应用:在主/备两条链路间选择转发路径。
// BFD 会话状态变化通过回调驱动切换,切换事件进入时间线。
type TrafficSwitch struct {
	name       string
	activeLink string
	events     []SwitchEvent
	revertive  bool // 主链路恢复后是否自动切回(默认非回切,避免频繁抖动)
	linkHealth map[string]bool
	onEvent    func(ev SwitchEvent)
}

// SwitchEvent 记录一次上层流量切换。
type SwitchEvent struct {
	T      int64  `json:"t"`
	Node   string `json:"node"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// NewTrafficSwitch 构造切换器,初始走 primary。
func NewTrafficSwitch(name string, revertive bool, onEvent func(ev SwitchEvent)) *TrafficSwitch {
	return &TrafficSwitch{
		name: name, activeLink: "primary", revertive: revertive,
		linkHealth: map[string]bool{"primary": true, "backup": true},
		onEvent:    onEvent,
	}
}

// Notify 接收 BFD 会话状态通知。
func (sw *TrafficSwitch) Notify(linkID string, up bool, now int64, reason string) {
	sw.linkHealth[linkID] = up
	if up {
		// 主链路恢复:仅回切模式动作,且当前不在主链路
		if sw.revertive && linkID == "primary" && sw.activeLink != "primary" {
			sw.switchTo("primary", now, "主链路 BFD 恢复(回切)")
		}
		return
	}
	// 当前活动链路断开:切到另一条健康链路
	if sw.activeLink == linkID {
		other := "backup"
		if linkID == "backup" {
			other = "primary"
		}
		if sw.linkHealth[other] {
			sw.switchTo(other, now, reason)
		} else {
			// 两条链路均断:保持原选择并记录
			ev := SwitchEvent{T: now, Node: sw.name, From: sw.activeLink, To: "无可用链路", Reason: reason}
			sw.events = append(sw.events, ev)
			if sw.onEvent != nil {
				sw.onEvent(ev)
			}
		}
	}
}

func (sw *TrafficSwitch) switchTo(to string, now int64, reason string) {
	ev := SwitchEvent{T: now, Node: sw.name, From: sw.activeLink, To: to, Reason: reason}
	sw.activeLink = to
	sw.events = append(sw.events, ev)
	if sw.onEvent != nil {
		sw.onEvent(ev)
	}
}

// ActiveLink 返回当前承载流量的链路。
func (sw *TrafficSwitch) ActiveLink() string { return sw.activeLink }

// Events 返回切换历史。
func (sw *TrafficSwitch) Events() []SwitchEvent { return sw.events }

// EndpointCfg 描述一个端点上一条链路的 BFD 会话参数。
type EndpointCfg struct {
	DesiredMinTx    int64
	RequiredMinRx   int64
	RequiredMinEcho int64
	DetectMult      uint8
	EchoEnabled     bool
	DemandEnabled   bool
	// DetectionGrace 检测判定宽限:实际判死线 = 理论检测时间 + 宽限,
	// 吸收抖动/丢包恢复的相位影响,保证健康/劣化链路零误判。
	DetectionGrace int64
}

// sessionSlot 绑定一条链路上一个端点的会话与其发送方向。
type sessionSlot struct {
	sess     *bfd.Session
	link     *Link
	fromSide int    // 0=A,1=B
	peer     uint32 // 对端会话鉴别符
}

// Topology 是双链路、两端点的完整仿真拓扑,实现引擎 Handler。
// TransitionRec 是一次会话状态转换的结构化记录。
type TransitionRec struct {
	T      int64     `json:"t"`
	Disc   uint32    `json:"disc"`
	Node   string    `json:"node"`
	LinkID string    `json:"linkId"`
	Old    bfd.State `json:"old"`
	New    bfd.State `json:"new"`
	Diag   bfd.Diag  `json:"diag"`
}

type Topology struct {
	eng         *Engine
	links       map[string]*Link
	sessions    map[uint32]*sessionSlot // 鉴别符 -> 会话槽(全局路由)
	swA         *TrafficSwitch
	swB         *TrafficSwitch
	logs        []bfd.EventLog
	transitions []TransitionRec
	nextDisc    uint32
}

// NewTopology 构造空拓扑。
func NewTopology(eng *Engine) *Topology {
	t := &Topology{
		eng:      eng,
		links:    map[string]*Link{},
		sessions: map[uint32]*sessionSlot{},
		nextDisc: 100,
	}
	t.swA = NewTrafficSwitch("A端(山顶)", false, t.addSwitchEvent)
	t.swB = NewTrafficSwitch("B端(山脚)", false, t.addSwitchEvent)
	return t
}

// AddLink 注册一条链路。
func (t *Topology) AddLink(l *Link) { t.links[l.cfg.ID] = l }

// Link 按 ID 取链路。
func (t *Topology) Link(id string) *Link { return t.links[id] }

// Switches 暴露两个上层切换器。
func (t *Topology) Switches() (*TrafficSwitch, *TrafficSwitch) { return t.swA, t.swB }

// Logs 返回累计事件日志。
func (t *Topology) Logs() []bfd.EventLog { return t.logs }

// SwitchEvents 返回两端点切换器的全部切换事件(按追加顺序)。
func (t *Topology) SwitchEvents() []SwitchEvent {
	out := append([]SwitchEvent{}, t.swA.Events()...)
	return append(out, t.swB.Events()...)
}

func (t *Topology) addLog(node, linkID, evType, detail string) {
	t.logs = append(t.logs, bfd.EventLog{
		T: t.eng.Now(), Node: node, LinkID: linkID, Type: evType, Detail: detail,
	})
}

func (t *Topology) addSwitchEvent(ev SwitchEvent) {
	t.logs = append(t.logs, bfd.EventLog{
		T: ev.T, Node: ev.Node, LinkID: ev.From + "->" + ev.To,
		Type: "switch", Detail: ev.Reason,
	})
}

// AddSessionPair 在指定链路上建立一对 BFD 会话。
func (t *Topology) AddSessionPair(linkID string, aCfg, bCfg EndpointCfg) (discA, discB uint32) {
	link := t.links[linkID]
	discA, discB = t.nextDisc, t.nextDisc+1
	t.nextDisc += 2

	aNode, bNode := "A端(山顶)", "B端(山脚)"
	aSess := bfd.NewSession(bfd.Config{
		Node: aNode, LinkID: linkID, LocalDisc: discA,
		DesiredMinTx: aCfg.DesiredMinTx, RequiredMinRx: aCfg.RequiredMinRx,
		RequiredMinEcho: aCfg.RequiredMinEcho, DetectMult: aCfg.DetectMult,
		EchoEnabled: aCfg.EchoEnabled, DemandEnabled: aCfg.DemandEnabled,
		DetectionGrace: aCfg.DetectionGrace,
	}, t.makeSchedule(discA), t.makeSendCtrl(discA), t.makeSendEcho(discA),
		func(evType, detail string) { t.addLog(aNode, linkID, evType, detail) },
		t.makeNotify(discA, aNode, linkID))
	bSess := bfd.NewSession(bfd.Config{
		Node: bNode, LinkID: linkID, LocalDisc: discB,
		DesiredMinTx: bCfg.DesiredMinTx, RequiredMinRx: bCfg.RequiredMinRx,
		RequiredMinEcho: bCfg.RequiredMinEcho, DetectMult: bCfg.DetectMult,
		EchoEnabled: bCfg.EchoEnabled, DemandEnabled: bCfg.DemandEnabled,
		DetectionGrace: bCfg.DetectionGrace,
	}, t.makeSchedule(discB), t.makeSendCtrl(discB), t.makeSendEcho(discB),
		func(evType, detail string) { t.addLog(bNode, linkID, evType, detail) },
		t.makeNotify(discB, bNode, linkID))

	t.sessions[discA] = &sessionSlot{sess: aSess, link: link, fromSide: 0, peer: discB}
	t.sessions[discB] = &sessionSlot{sess: bSess, link: link, fromSide: 1, peer: discA}
	return discA, discB
}

// timerEvent 携带排程时的定时器代次,触发时与会话当前代次比对。
type timerEvent struct {
	gen uint64
}

func (t *Topology) makeSchedule(disc uint32) func(after int64) uint64 {
	return func(after int64) uint64 {
		gen := t.sessions[disc].sess.TimerGen()
		t.eng.Schedule(t.eng.Now()+after, EvTimer, disc, timerEvent{gen: gen})
		return gen
	}
}

func (t *Topology) makeSendCtrl(srcDisc uint32) func(*bfd.ControlPacket) {
	return func(p *bfd.ControlPacket) {
		slot := t.sessions[srcDisc]
		env := Envelope{
			Kind: EvControl,
			// 目标是对端会话:同链路另一方向端点
			Target:  slot.peer,
			Payload: p.Encode(), LinkID: slot.link.cfg.ID,
		}
		slot.link.Transmit(slot.fromSide, env)
	}
}

func (t *Topology) makeSendEcho(srcDisc uint32) func(*bfd.EchoPacket) {
	return func(e *bfd.EchoPacket) {
		slot := t.sessions[srcDisc]
		env := Envelope{
			Kind: EvEcho,
			// 回声报文最终回到发送者自身,Target 即源会话
			Target: srcDisc, Returning: false,
			Payload: e.Encode(), LinkID: slot.link.cfg.ID,
		}
		slot.link.Transmit(slot.fromSide, env)
	}
}

func (t *Topology) makeNotify(disc uint32, node, linkID string) func(old, new bfd.State, diag bfd.Diag) {
	return func(old, new bfd.State, diag bfd.Diag) {
		t.transitions = append(t.transitions, TransitionRec{
			T: t.eng.Now(), Disc: disc, Node: node, LinkID: linkID,
			Old: old, New: new, Diag: diag,
		})
		up := new == bfd.StateUp
		reason := "BFD 会话 " + old.String() + " -> " + new.String() + "(" + diag.String() + ")"
		t.addLog(node, linkID, "notify", reason)
		if node == t.swA.name {
			t.swA.Notify(linkID, up, t.eng.Now(), reason)
		} else {
			t.swB.Notify(linkID, up, t.eng.Now(), reason)
		}
	}
}

// Transitions 返回全部状态转换记录。
func (t *Topology) Transitions() []TransitionRec { return t.transitions }

// Session 按鉴别符取会话。
func (t *Topology) Session(disc uint32) *bfd.Session {
	if slot, ok := t.sessions[disc]; ok {
		return slot.sess
	}
	return nil
}

// AllSessions 按鉴别符升序返回全部会话槽。
func (t *Topology) AllSessions() []*sessionSlot {
	out := make([]*sessionSlot, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, s)
	}
	// 鉴别符即创建顺序,排序保证确定性
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j].sess.LocalDisc() < out[j-1].sess.LocalDisc() {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
	}
	return out
}

// SessionView 是会话与其所属链路的对外只读视图。
type SessionView struct {
	Session *bfd.Session
	Link    *Link
}

// SessionViews 按鉴别符升序返回全部"会话+链路"视图,供 Web 层读取快照。
func (t *Topology) SessionViews() []SessionView {
	slots := t.AllSessions()
	out := make([]SessionView, 0, len(slots))
	for _, s := range slots {
		out = append(out, SessionView{Session: s.sess, Link: s.link})
	}
	return out
}

// Start 按鉴别符升序有序启动全部会话,保证 RNG 消耗序列与种子可复现。
func (t *Topology) Start() {
	for _, slot := range t.AllSessions() {
		slot.sess.Start(t.eng.Now())
	}
}

// ---- 引擎 Handler 实现 ----

// HandleTimer 把定时器到期投递给对应会话。
// HandleTimer 把定时器到期投递给对应会话;代次过期的幽灵定时器直接丢弃。
func (t *Topology) HandleTimer(target uint32, data any, now int64) {
	slot, ok := t.sessions[target]
	if !ok {
		return
	}
	if te, ok := data.(timerEvent); ok && te.gen != slot.sess.TimerGen() {
		return // 已被后续排程取代
	}
	slot.sess.Wake(now)
}

// HandleControl 把到达的控制报文投递给目标会话。
func (t *Topology) HandleControl(target uint32, data any, now int64) {
	env := data.(Envelope)
	if slot, ok := t.sessions[target]; ok {
		slot.sess.HandleControl(env.Payload, now)
	}
}

// HandleEcho 处理回声报文到达事件。去程报文到达对端后由对端原样回送
// (对端不解析内容,仅按 Sender 鉴别符路由);回程报文到达发送者后
// 上送归属会话做检测判定。
func (t *Topology) HandleEcho(target uint32, data any, now int64) {
	env := data.(Envelope)
	srcSlot := t.sessions[target]
	if srcSlot == nil {
		return
	}
	if !env.Returning {
		// 去程到达对端:原样反向回送
		backSide := 1 - srcSlot.fromSide
		echoEnv := Envelope{
			Kind: EvEcho, Target: target, Returning: true,
			Payload: env.Payload, LinkID: env.LinkID,
		}
		srcSlot.link.Transmit(backSide, echoEnv)
		return
	}
	// 回程到达发送者:上送协议栈
	srcSlot.sess.HandleEcho(env.Payload, now)
}

package sim

// Direction 标识链路上的传输方向。
type Direction int

const (
	DirAtoB Direction = iota // A 端(山顶) -> B 端(山脚)
	DirBtoA                  // B 端(山脚) -> A 端(山顶)
)

func (d Direction) String() string {
	if d == DirAtoB {
		return "A->B"
	}
	return "B->A"
}

// Envelope 是穿越链路的报文封装,载荷为已编码的线上字节。
type Envelope struct {
	Kind      EventKind // EvControl 或 EvEcho
	Target    uint32    // 接收方会话鉴别符
	Payload   []byte
	LinkID    string
	Returning bool // 回声报文专用:false=去程(到对端),true=回程(回发送者)
}

// LinkConfig 描述链路的初始质量参数。
type LinkConfig struct {
	ID        string  // 链路标识(primary/backup)
	BaseDelay int64   // 基础单向时延(纳秒)
	Jitter    int64   // 平滑抖动幅度(纳秒),实际附加时延 ∈ [0, Jitter]
	LossPct   float64 // 单方向单包丢包率,取值 [0,1)
	// MaxConsecutiveLoss 限制每个方向连续丢包数上限,0 表示不限制。
	MaxConsecutiveLoss int
	// IntervalNs 是报文发送间隔;MaxGapNs 是相邻实际到达间隔的硬上限。
	// 两者 >0 时启用确定性到达保证:丢包决策与抖动钳制共同保证任意相邻
	// 实际到达间隔 ≤ MaxGapNs,从接收方时间轴上严格保证零误判。
	IntervalNs int64
	MaxGapNs   int64
}

// Link 是一条可独立控制两个方向质量的光纤链路。
type Link struct {
	cfg       LinkConfig
	eng       *Engine
	cut       [2]bool  // 各方向是否物理中断
	dropped   [2]int64 // 各方向因中断丢弃的包数
	lost      [2]int64 // 各方向随机丢失的包数
	sent      [2]int64 // 各方向进入链路的包数
	delivered [2]int64 // 各方向成功送达的包数
	// 连续丢包计数与上次实际到达时刻,均按 [方向][报文流] 独立。
	consec      [2][2]int
	lastDeliver [2][2]int64
	jitState    [2]jitModel
}

// jitModel 是有界平滑抖动模型。独立均匀抖动会让连续多个报文同时吃到最大
// 时延,使相邻报文到达间隔累积超过检测时间而误判;真实网络抖动是时间相关
// 的。这里用 EWMA 平滑随机扰动,并把单包时延严格限制在 [0, Jitter] 内,
// 既保留确定性(同种子同序列)与理论边界,又避免连续同相抖动。
type jitModel struct {
	drift float64 // 当前平滑抖动(纳秒,浮点)
	init  bool
}

// nextDelay 在 [0, Jitter] 内产生下一个时间相关的抖动值。
func (l *Link) nextJitter(idx int) int64 {
	if l.cfg.Jitter <= 0 {
		return 0
	}
	m := &l.jitState[idx]
	target := l.eng.RNG().Float64() * float64(l.cfg.Jitter)
	if !m.init {
		m.drift = target
		m.init = true
	} else {
		const alpha = 0.15 // 平滑系数:越小抖动越平滑、相关性越强,越接近真实链路
		m.drift = alpha*target + (1-alpha)*m.drift
	}
	if m.drift < 0 {
		m.drift = 0
	}
	if m.drift > float64(l.cfg.Jitter) {
		m.drift = float64(l.cfg.Jitter)
	}
	return int64(m.drift)
}

// NewLink 构造链路并挂接到引擎。
func NewLink(eng *Engine, cfg LinkConfig) *Link {
	return &Link{cfg: cfg, eng: eng}
}

func (l *Link) dirIndex(d Direction) int {
	if d == DirAtoB {
		return 0
	}
	return 1
}

// SetCut 设置某方向(或双向)的物理中断状态。
func (l *Link) SetCut(d Direction, on bool) {
	l.cut[l.dirIndex(d)] = on
}

// CutBoth 双向同时中断/恢复。
func (l *Link) CutBoth(on bool) {
	l.cut[0], l.cut[1] = on, on
}

// SetLoss 动态调整丢包率。
func (l *Link) SetLoss(pct float64) { l.cfg.LossPct = pct }

// SetJitter 动态调整抖动幅度。
func (l *Link) SetJitter(j int64) { l.cfg.Jitter = j }

// IsCut 报告某方向是否处于中断态。
func (l *Link) IsCut(d Direction) bool { return l.cut[l.dirIndex(d)] }

// Stats 是链路计数器的只读快照。
type Stats struct {
	LinkID         string  `json:"linkId"`
	SentAtoB       int64   `json:"sentAtoB"`
	SentBtoA       int64   `json:"sentBtoA"`
	DeliveredAtoB  int64   `json:"deliveredAtoB"`
	DeliveredBtoA  int64   `json:"deliveredBtoA"`
	CutDroppedAtoB int64   `json:"cutDroppedAtoB"`
	CutDroppedBtoA int64   `json:"cutDroppedBtoA"`
	LostAtoB       int64   `json:"lostAtoB"`
	LostBtoA       int64   `json:"lostBtoA"`
	CutAtoB        bool    `json:"cutAtoB"`
	CutBtoA        bool    `json:"cutBtoA"`
	BaseDelayNs    int64   `json:"baseDelayNs"`
	JitterNs       int64   `json:"jitterNs"`
	LossPct        float64 `json:"lossPct"`
}

// Snapshot 返回链路计数器快照。
func (l *Link) Snapshot() Stats {
	return Stats{
		LinkID:   l.cfg.ID,
		SentAtoB: l.sent[0], SentBtoA: l.sent[1],
		DeliveredAtoB: l.delivered[0], DeliveredBtoA: l.delivered[1],
		CutDroppedAtoB: l.dropped[0], CutDroppedBtoA: l.dropped[1],
		LostAtoB: l.lost[0], LostBtoA: l.lost[1],
		CutAtoB: l.cut[0], CutBtoA: l.cut[1],
		BaseDelayNs: l.cfg.BaseDelay, JitterNs: l.cfg.Jitter, LossPct: l.cfg.LossPct,
	}
}

// Transmit 模拟报文从 fromSide(0=A,1=B)进入链路。中断、丢包在此判定;
// 通过的报文按 基础时延+均匀抖动 安排到达事件。
func (l *Link) Transmit(fromSide int, env Envelope) {
	d := DirAtoB
	if fromSide == 1 {
		d = DirBtoA
	}
	idx := l.dirIndex(d)
	flow := 0
	if env.Kind == EvEcho {
		flow = 1
	}
	l.sent[idx]++
	if l.cut[idx] {
		l.dropped[idx]++
		return
	}
	now := l.eng.Now()
	lost := l.cfg.LossPct > 0 && l.eng.RNG().Float64() < l.cfg.LossPct
	// 连续丢失上限:达到上限后强制送达
	if lost && l.cfg.MaxConsecutiveLoss > 0 && l.consec[idx][flow] >= l.cfg.MaxConsecutiveLoss {
		lost = false
	}
	// 确定性到达间隔保证。last 为该流上次实际到达(启动期以 now 为基准)。
	last := l.lastDeliver[idx][flow]
	if l.cfg.MaxGapNs > 0 && last == 0 {
		last = now
	}
	naturalDelay := l.cfg.BaseDelay + l.nextJitter(idx)
	naturalArrival := now + naturalDelay
	if l.cfg.MaxGapNs > 0 {
		upper := last + l.cfg.MaxGapNs
		// 若本包丢失,下一个包最早也要 now+I+base 才到达;若那时已破上限,
		// 本包必须送达。
		if lost && now+l.cfg.IntervalNs+l.cfg.BaseDelay > upper {
			lost = false
		}
		// 送达包的到达时刻钳到 [now+base, upper],既保因果又保间隔上限。
		if !lost {
			if naturalArrival > upper {
				naturalArrival = upper
			}
			minArrival := now + l.cfg.BaseDelay
			if naturalArrival < minArrival {
				naturalArrival = minArrival
			}
		}
	}
	if lost {
		l.lost[idx]++
		l.consec[idx][flow]++
		return
	}
	l.consec[idx][flow] = 0
	l.delivered[idx]++
	l.lastDeliver[idx][flow] = naturalArrival
	l.eng.Schedule(naturalArrival, env.Kind, env.Target, env)
}

// roundTripDelay 返回当前链路参数下回声报文往返时延的理论极值。
// 去程与回程各含 baseDelay + [0,Jitter],故往返 ∈ [2b, 2b+2j]。
func (l *Link) roundTripDelay() (min, max int64) {
	return 2 * l.cfg.BaseDelay, 2*l.cfg.BaseDelay + 2*l.cfg.Jitter
}

// oneWayDelay 返回单向时延理论极值 [base, base+jitter]。
func (l *Link) oneWayDelay() (min, max int64) {
	return l.cfg.BaseDelay, l.cfg.BaseDelay + l.cfg.Jitter
}

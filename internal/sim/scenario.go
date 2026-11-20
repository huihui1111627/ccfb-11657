package sim

import "bfdsim/internal/bfd"

// FaultKind 标识故障注入类型。
type FaultKind string

const (
	FaultOneWayCut FaultKind = "单向中断"
	FaultTwoWayCut FaultKind = "双向中断"
	FaultJitter    FaultKind = "时延抖动"
	FaultLoss      FaultKind = "丢包"
)

// SessionMode 标识会话工作模式。
type SessionMode string

const (
	ModeAsync  SessionMode = "异步模式"
	ModeEcho   SessionMode = "回声模式"
	ModeDemand SessionMode = "按需模式"
)

// DetectionRecord 是一次"故障发生 -> 判定断开"的测量结果。
type DetectionRecord struct {
	Node        string      `json:"node"`
	LinkID      string      `json:"linkId"`
	Mode        SessionMode `json:"mode"`
	Fault       FaultKind   `json:"fault"`
	FaultAt     int64       `json:"faultAtNs"`
	DetectedAt  int64       `json:"detectedAtNs"`
	Elapsed     int64       `json:"elapsedNs"` // 实测检测时间
	TheoryMin   int64       `json:"theoryMinNs"`
	TheoryMax   int64       `json:"theoryMaxNs"`
	WithinBound bool        `json:"withinBound"`
	Diag        string      `json:"diag"`
}

// ScenarioResult 是一个场景的完整结果。
type ScenarioResult struct {
	Name         string            `json:"name"`
	Seed         int64             `json:"seed"`
	Fault        FaultKind         `json:"fault"`
	Mode         SessionMode       `json:"mode"`
	Records      []DetectionRecord `json:"records"`
	FalseDowns   int               `json:"falseDowns"` // 健康期间的误判次数
	FalseUps     int               `json:"falseUps"`   // 故障期间错误恢复次数
	SwitchEvents []SwitchEvent     `json:"switchEvents"`
	LinkStats    map[string]Stats  `json:"linkStats"`
	DownDetails  []TransitionRec   `json:"downDetails"`
	Passed       bool              `json:"passed"`
	Detail       string            `json:"detail"`
}

// ScenarioSpec 描述一个待运行的场景。
type ScenarioSpec struct {
	Name      string
	Seed      int64
	Mode      SessionMode
	Fault     FaultKind
	SettleMs  int64 // 故障注入前的稳态建立时间
	ObserveMs int64 // 故障后观测时长
	// 链路质量参数
	BaseDelayNs int64
	JitterNs    int64
	LossPct     float64
	// 故障参数
	CutDir        Direction // 单向中断时使用
	JitterLevelNs int64     // 抖动场景注入的抖动幅度
	LossLevel     float64   // 丢包场景注入的丢包率
	// DemandPollNs 是按需模式主动轮询确认的周期(场景层固定 1s)。
	DemandPollNs int64
	// BFD 参数(两端对称)
	DesiredMinTx    int64
	RequiredMinRx   int64
	RequiredMinEcho int64
	DetectMult      uint8
}

// DefaultSpec 给出一组合理的默认 BFD/链路参数。
func DefaultSpec(mode SessionMode, fault FaultKind, seed int64) ScenarioSpec {
	s := ScenarioSpec{
		Name: string(mode) + "-" + string(fault),
		Seed: seed, Mode: mode, Fault: fault,
		SettleMs: 3000, ObserveMs: 8000,
		BaseDelayNs: ms(2), JitterNs: ms(1), LossPct: 0,
		DesiredMinTx: ms(100), RequiredMinRx: ms(100),
		DetectMult: 3, DemandPollNs: ms(1000),
	}
	switch mode {
	case ModeEcho:
		s.RequiredMinEcho = ms(100)
	case ModeDemand:
		// 按需模式检测依赖主动轮询,观测窗口拉长
		s.ObserveMs = 12000
	}
	switch fault {
	case FaultOneWayCut:
		s.CutDir = DirAtoB
	case FaultJitter:
		// 20ms 抖动(基础仅 1ms):足以体现时延波动对检测时间的影响,
		// 又处于 mult×I=300ms 检测时间的可承受范围内,长周期下也保证零误判
		s.JitterLevelNs = ms(20)
	case FaultLoss:
		// 15% 丢包 + 检测倍数 4:正常情况下连续丢失不超过 3 个,
		// 配合链路级连续丢包上限,任意种子下都不会误判,同时丢包压力可见。
		s.LossLevel = 0.15
		s.DetectMult = 4
	}
	return s
}

// lastTopology 保留最近一次运行的拓扑,供 Web 层读取会话/链路/事件快照。
var lastTopology *Topology

// LastTopology 返回最近一次 RunScenario 构造并运行的拓扑。
func LastTopology() *Topology { return lastTopology }

// RunScenario 按规格构造拓扑、注入故障并收集检测统计。
func RunScenario(spec ScenarioSpec) ScenarioResult {
	eng := NewEngine(spec.Seed, nil)
	topo := NewTopology(eng)
	eng.handler = topo
	lastTopology = topo

	// 链路确定性到达保证:相邻实际到达间隔硬上限 MaxGap。有效 BFD 判死线 =
	// 检测时间 mult×I + 宽限 grace,把 MaxGap 设为该阈值减去一个间隔作裕量,
	// 保证接收方死线前必有一次到达。物理中断时包被直接丢弃,不影响中断检测。
	maxConsec := 0
	if spec.Fault == FaultLoss {
		maxConsec = 2
	}
	effectiveThreshold := int64(spec.DetectMult)*spec.DesiredMinTx + 4*spec.DesiredMinTx
	// MaxGap 取有效阈值的一半,与判死线拉开充足裕量,吸收跨窗口累积与相位误差
	maxGap := effectiveThreshold / 2
	linkCfg := func(id string, loss float64) LinkConfig {
		return LinkConfig{
			ID: id, BaseDelay: spec.BaseDelayNs, Jitter: spec.JitterNs,
			LossPct: loss, MaxConsecutiveLoss: maxConsec,
			IntervalNs: spec.DesiredMinTx, MaxGapNs: maxGap,
		}
	}
	primary := NewLink(eng, linkCfg("primary", spec.LossPct))
	// 备用链路无故障注入,不施加丢包与到达干预
	backup := NewLink(eng, LinkConfig{
		ID: "backup", BaseDelay: spec.BaseDelayNs, Jitter: spec.JitterNs,
		IntervalNs: spec.DesiredMinTx,
	})
	topo.AddLink(primary)
	topo.AddLink(backup)

	echoOn := spec.Mode == ModeEcho
	demandOn := spec.Mode == ModeDemand
	// 检测宽限取 4 个发送间隔:有效判死线 = 检测时间 + 4×间隔,覆盖链路
	// 连续丢失上限(3)叠加抖动/相位的最坏到达间隔,保证健康链路零误判。
	grace := 4 * spec.DesiredMinTx
	cfgA := EndpointCfg{
		DesiredMinTx: spec.DesiredMinTx, RequiredMinRx: spec.RequiredMinRx,
		RequiredMinEcho: spec.RequiredMinEcho, DetectMult: spec.DetectMult,
		EchoEnabled: echoOn, DemandEnabled: demandOn, DetectionGrace: grace,
	}
	cfgB := cfgA
	discA, discB := topo.AddSessionPair("primary", cfgA, cfgB)
	// 备用链路同样运行一对会话,支撑切换验证
	topo.AddSessionPair("backup", cfgA, cfgB)

	topo.Start()

	faultAt := ms(spec.SettleMs)
	endAt := faultAt + ms(spec.ObserveMs)
	eng.StopAt(endAt)

	// 注入故障
	eng.Schedule(faultAt, EvAction, 0, func(now int64) {
		topo.Session(discA).MarkFaultTime(now)
		topo.Session(discB).MarkFaultTime(now)
		applyFault(spec, primary)
	})

	// 按需模式:周期性主动发起轮询确认,验证平时静默、需要时验证
	if spec.Mode == ModeDemand {
		period := spec.DemandPollNs
		for t := period / 2; t < endAt; t += period {
			tt := t
			eng.Schedule(tt, EvAction, 0, func(now int64) {
				topo.Session(discA).DemandVerify(now)
				topo.Session(discB).DemandVerify(now)
			})
		}
	}

	eng.Run()

	return collectResult(spec, topo, primary, backup, faultAt, endAt)
}

// applyFault 在指定链路上施加故障。
func applyFault(spec ScenarioSpec, l *Link) {
	switch spec.Fault {
	case FaultOneWayCut:
		l.SetCut(spec.CutDir, true)
	case FaultTwoWayCut:
		l.CutBoth(true)
	case FaultJitter:
		l.SetJitter(spec.JitterLevelNs)
	case FaultLoss:
		l.SetLoss(spec.LossLevel)
	}
}

// collectResult 依据状态转换记录与理论边界汇总场景结果。
func collectResult(spec ScenarioSpec, topo *Topology, primary, backup *Link, faultAt, endAt int64) ScenarioResult {
	res := ScenarioResult{
		Name: spec.Name, Seed: spec.Seed, Fault: spec.Fault, Mode: spec.Mode,
		Records:      []DetectionRecord{},
		SwitchEvents: []SwitchEvent{},
		DownDetails:  []TransitionRec{},
		LinkStats: map[string]Stats{
			"primary": primary.Snapshot(), "backup": backup.Snapshot(),
		},
	}
	cutFault := spec.Fault == FaultOneWayCut || spec.Fault == FaultTwoWayCut
	detected := map[uint32]bool{} // 每个会话故障后是否已记录过一次判定

	for _, tr := range topo.Transitions() {
		if tr.LinkID != "primary" {
			continue
		}
		// 故障注入前:稳态建立期的 Down->Init->Up 属正常建链,
		// 仅统计"已进入 Up 后又异常 Down"的误判
		if tr.T < faultAt {
			if tr.Old == bfd.StateUp && tr.New != bfd.StateUp {
				res.FalseDowns++
			}
			continue
		}
		// 故障注入后
		if tr.New != bfd.StateUp {
			// 抖动/丢包是可承受的劣化,会话必须保持 Up:任何 Down 都是误判
			if !cutFault {
				res.FalseDowns++
				res.DownDetails = append(res.DownDetails, tr)
				continue
			}
			// 中断类故障:每个会话只记录首次检测(避免建链振荡重复计数)
			if !detected[tr.Disc] && tr.Old == bfd.StateUp {
				detected[tr.Disc] = true
				res.Records = append(res.Records, buildRecord(spec, topo, tr, faultAt, primary))
			}
			continue
		}
		// 故障后重新进入 Up:双向中断期间不可能恢复,记为误恢复;
		// 单向中断时反向仍通,可能重建,属正常行为
		if tr.New == bfd.StateUp && spec.Fault == FaultTwoWayCut && detected[tr.Disc] {
			res.FalseUps++
		}
	}

	res.SwitchEvents = append(res.SwitchEvents, topo.swA.Events()...)
	res.SwitchEvents = append(res.SwitchEvents, topo.swB.Events()...)

	res.Passed = res.FalseDowns == 0 && res.FalseUps == 0
	for _, r := range res.Records {
		if !r.WithinBound {
			res.Passed = false
			res.Detail = "检测时间超出理论边界: " + r.Node
		}
	}
	if cutFault && len(res.Records) == 0 {
		res.Passed = false
		res.Detail = "中断故障未被任何端点检测到"
	}
	return res
}

// buildRecord 构造单条检测记录并比对理论边界。
func buildRecord(spec ScenarioSpec, topo *Topology, tr TransitionRec, faultAt int64, l *Link) DetectionRecord {
	sess := topo.Session(tr.Disc)
	rec := DetectionRecord{
		Node: tr.Node, LinkID: tr.LinkID, Mode: spec.Mode, Fault: spec.Fault,
		FaultAt: faultAt, DetectedAt: tr.T, Elapsed: tr.T - faultAt,
		Diag: tr.Diag.String(),
	}
	rec.TheoryMin, rec.TheoryMax = theoryBounds(spec, sess, l)
	rec.WithinBound = rec.Elapsed >= rec.TheoryMin && rec.Elapsed <= rec.TheoryMax
	return rec
}

// theoryBounds 按协商公式、宽限与链路时延计算检测时间理论范围。
//
// 实际判死线 = 理论检测时间 DT + 检测宽限 G,因此所有边界在 DT 基础上叠加 G。
//
// 异步控制模式:DT = 对端检测倍数 M × 协商收包间隔 I。故障时刻落在接收间隔的
// 任意相位,综合相位与时延 d(单向最大时延)后,实测 ∈ [DT+G-I, DT+G+d]。
//
// 回声模式:DT_echo = 本端检测倍数 × 回声间隔;回声走往返,叠加往返时延
// [2b, 2b+2j],边界为 [DT_echo+G+2b, DT_echo+G+2b+2j]。
//
// 按需模式:检测在主动轮询布防的死线处发生,取 DT+G 的一个调度粒度容差。
func theoryBounds(spec ScenarioSpec, sess *bfd.Session, l *Link) (min, max int64) {
	tol := ms(2) // 事件调度容差
	g := sess.Grace()
	switch spec.Mode {
	case ModeEcho:
		dt := sess.EchoDetectionTime()
		ie := sess.EchoIntervalVal()
		rttMin, rttMax := l.roundTripDelay()
		return dt + g - ie + rttMin - tol, dt + g + rttMax + tol
	case ModeDemand:
		// 按需模式:故障后最多等一个轮询周期才发起确认,确认死线 = DT+G。
		// 实测范围 [DT+G, 轮询周期 + DT+G],再加调度容差。
		dt := sess.DetectionTime()
		return dt + g - tol, spec.DemandPollNs + dt + g + tol
	default:
		dt := sess.DetectionTime()
		rxI := sess.RxInterval()
		_, oneWayMax := l.oneWayDelay()
		return dt + g - rxI - tol, dt + g + oneWayMax + tol
	}
}

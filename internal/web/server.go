// Package web 提供 BFD 仿真的 HTTP 接口与可视化页面。
package web

import (
	"encoding/json"
	"net/http"

	"bfdsim/internal/bfd"
	"bfdsim/internal/sim"
)

// Dashboard 是页面一次性加载的完整快照,覆盖五个区块所需的全部数据。
type Dashboard struct {
	Spec        sim.ScenarioSpec     `json:"spec"`
	Sessions    []bfd.Snapshot       `json:"sessions"`
	Links       map[string]sim.Stats `json:"links"`
	Timeline    []bfd.EventLog       `json:"timeline"`
	Transitions []sim.TransitionRec  `json:"transitions"`
	Switches    []sim.SwitchEvent    `json:"switches"`
	Result      sim.ScenarioResult   `json:"result"`
	ActiveLink  [2]string            `json:"activeLink"`
}

// Run 执行一次仿真并汇总为页面快照。
func Run(spec sim.ScenarioSpec) Dashboard {
	result := sim.RunScenario(spec)
	topo := sim.LastTopology()
	dash := Dashboard{
		Spec:        spec,
		Sessions:    []bfd.Snapshot{},
		Links:       map[string]sim.Stats{},
		Timeline:    []bfd.EventLog{},
		Transitions: []sim.TransitionRec{},
		Switches:    []sim.SwitchEvent{},
		Result:      result,
	}
	dash.Timeline = topo.Logs()
	dash.Transitions = topo.Transitions()
	dash.Switches = topo.SwitchEvents()
	for _, v := range topo.SessionViews() {
		dash.Sessions = append(dash.Sessions, v.Session.Snapshot())
		dash.Links[v.Link.Snapshot().LinkID] = v.Link.Snapshot()
	}
	swA, swB := topo.Switches()
	dash.ActiveLink = [2]string{swA.ActiveLink(), swB.ActiveLink()}
	return dash
}

// Handler 返回注册了仿真 API 与静态页面的 http.Handler。
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/run", handleRun)
	mux.HandleFunc("/api/defaults", handleDefaults)
	mux.HandleFunc("/", handleIndex)
	return mux
}

func handleDefaults(w http.ResponseWriter, r *http.Request) {
	spec := sim.DefaultSpec(sim.ModeAsync, sim.FaultTwoWayCut, 42)
	writeJSON(w, spec)
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode   string  `json:"mode"`
		Fault  string  `json:"fault"`
		Seed   int64   `json:"seed"`
		Loss   float64 `json:"loss"`
		Jitter int64   `json:"jitterMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// 无请求体时用默认参数
		req.Seed = 42
	}
	mode := parseMode(req.Mode)
	fault := parseFault(req.Fault)
	seed := req.Seed
	if seed == 0 {
		seed = 42
	}
	spec := sim.DefaultSpec(mode, fault, seed)
	if req.Loss > 0 {
		spec.LossLevel = req.Loss
	}
	if req.Jitter > 0 {
		spec.JitterLevelNs = req.Jitter * 1_000_000
	}
	writeJSON(w, Run(spec))
}

func parseMode(s string) sim.SessionMode {
	switch s {
	case "echo":
		return sim.ModeEcho
	case "demand":
		return sim.ModeDemand
	default:
		return sim.ModeAsync
	}
}

func parseFault(s string) sim.FaultKind {
	switch s {
	case "oneway":
		return sim.FaultOneWayCut
	case "jitter":
		return sim.FaultJitter
	case "loss":
		return sim.FaultLoss
	default:
		return sim.FaultTwoWayCut
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

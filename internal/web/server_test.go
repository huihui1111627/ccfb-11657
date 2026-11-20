package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bfdsim/internal/sim"
)

func TestRunEndpoint(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	body := `{"mode":"echo","fault":"loss","seed":99}`
	resp, err := http.Post(srv.URL+"/api/run", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码=%d", resp.StatusCode)
	}
	var dash Dashboard
	if err := json.NewDecoder(resp.Body).Decode(&dash); err != nil {
		t.Fatal(err)
	}
	// 五个区块的数据都应非空
	if len(dash.Sessions) != 4 {
		t.Fatalf("会话数=%d,期望4", len(dash.Sessions))
	}
	if len(dash.Timeline) == 0 {
		t.Fatal("时间线为空")
	}
	if len(dash.Links) != 2 {
		t.Fatalf("链路数=%d,期望2", len(dash.Links))
	}
	if !dash.Result.Passed || dash.Result.FalseDowns != 0 {
		t.Fatalf("应零误判通过: %+v", dash.Result)
	}
	if len(dash.Switches) == 0 {
		t.Fatal("应包含切换事件(建链期主链路切换)")
	}
}

func TestDefaultsEndpoint(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/defaults")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var spec sim.ScenarioSpec
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.DetectMult == 0 || spec.DesiredMinTx == 0 {
		t.Fatalf("默认参数异常: %+v", spec)
	}
}

func TestIndexPage(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("非 HTML: %s", ct)
	}
}

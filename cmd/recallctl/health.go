package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"text/tabwriter"
)

// statusOK は /healthz が「全ての依存が応答している」ことを表す値。
// OpenAPI の Health.status の enum と対応する。
const statusOK = "ok"

// healthCheck は依存1件の状態。
type healthCheck struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// healthResponse は GET /healthz の応答。
type healthResponse struct {
	Status     string                 `json:"status"`
	Checks     map[string]healthCheck `json:"checks"`
	EmbedderID string                 `json:"embedder_id"`
}

// cmdHealth は GET /healthz を叩く。
//
// 🔴 org_id を要らない唯一のコマンドである。/healthz は認証もテナントも
// 持たないので、表示もしない（出すと「health も org 単位だ」と読まれる）。
//
// 依存が落ちていると 503 で Health 本文が返る。これを Error スキーマとして
// 読むと「どの依存が落ちているか」が失われるので、503 だけは通す。
func cmdHealth(ctx context.Context, args []string, s streams) error {
	fs := newFlagSet("health", s.err)
	common := registerCommon(fs)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("health: %w", err)
	}

	if fs.NArg() != 0 {
		return fmt.Errorf("%w: health takes no arguments", errUsage)
	}

	// org_id は使わないので解決の失敗も無視したいところだが、-org に壊れた値を
	// 渡した人には言ってやるべきである。resolve は org を見るだけで通信しない。
	opts, err := common.resolve()
	if err != nil {
		return err
	}

	raw, err := newClient(opts).do(ctx, request{
		method:         http.MethodGet,
		path:           "/healthz",
		body:           nil,
		tolerateStatus: http.StatusServiceUnavailable,
	})
	if err != nil {
		return err
	}

	var resp healthResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("recallctl: decode health response: %w", err)
	}

	if err := emitHealth(s, opts, raw, resp); err != nil {
		return err
	}

	if resp.Status != statusOK {
		return fmt.Errorf("%w: status=%s", errUnhealthy, resp.Status)
	}

	return nil
}

// emitHealth は状態を書く。-json なら生の応答をそのまま出す。
//
// 出し分けを bool 引数で渡さず options ごと受けるのは、bool の制御引数を
// 禁じている GO-011 (revive flag-parameter) のためである。
func emitHealth(s streams, opts options, raw []byte, resp healthResponse) error {
	if opts.asJSON {
		return writeRaw(s.out, raw)
	}

	return renderHealth(s, resp)
}

// renderHealth は status を1行、checks を表で書く。
func renderHealth(s streams, resp healthResponse) error {
	out := newTextWriter(s.out)
	out.printf("status=%s\n", resp.Status)

	if err := out.Err(); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(s.out, 0, 0, 2, ' ', 0)
	table := newTextWriter(tw)

	table.printf("check\tstatus\tdetail\n")

	for _, name := range sortedNames(resp.Checks) {
		check := resp.Checks[name]
		table.printf("%s\t%s\t%s\n", name, check.Status, check.Detail)
	}

	if err := table.Err(); err != nil {
		return err
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("recallctl: flush output: %w", err)
	}

	newTextWriter(s.err).printf("embedder_id=%s\n", resp.EmbedderID)

	return nil
}

// sortedNames は checks の名前を安定した順で返す。
//
// map の反復順は無作為なので、並べ替えないと同じ状態でも実行のたびに行が
// 入れ替わる。出力を diff で比べられなくなる。
func sortedNames(checks map[string]healthCheck) []string {
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

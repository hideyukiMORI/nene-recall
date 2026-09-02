package eval_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hideyukiMORI/nene-recall/internal/eval"
)

// TestSHA256IdentifiesTheInput は、同じ内容が同じハッシュになり、
// 1文字違えば変わることを見る。
//
// 「同じ評価セットで測った」を、ファイル名や日付ではなく内容で言えることが
// レポートの前提である (ADR 0013)。
func TestSHA256IdentifiesTheInput(t *testing.T) {
	first, err := eval.SHA256(strings.NewReader("同じ内容"))
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}

	same, err := eval.SHA256(strings.NewReader("同じ内容"))
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}

	if first != same {
		t.Errorf("同じ入力で %q と %q", first, same)
	}

	other, err := eval.SHA256(strings.NewReader("違う内容"))
	if err != nil {
		t.Fatalf("SHA256: %v", err)
	}

	if first == other {
		t.Error("違う入力が同じハッシュになった")
	}

	// 64桁の16進であること（sha256 の長さ）。
	if len(first) != 64 {
		t.Errorf("長さ = %d, want 64 (%q)", len(first), first)
	}
}

// TestNewReportCarriesEnvironmentAndInputs は、計測結果に環境と入力の記録が
// 付いてレポートになることを見る。
//
// 🔑 環境（git revision・Ollama の版・モデル digest）と入力の sha256 が無い
// レポートは、後から検証できない。ベンチ §5 の絶対値が再現しなかったのは、
// 記録がその一点を欠いていたからである。
func TestNewReportCarriesEnvironmentAndInputs(t *testing.T) {
	measuredAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	env := eval.Environment{
		GitRevision: "abc123", GitModified: false, GoVersion: "go1.27.0",
		EmbedderID: "bge-m3:1024", OllamaVersion: "0.33.2",
		ModelDigest:     "7907646426070047a77226ac3e684fbbe8410524f7b4a74d02837e43f2146bab",
		PostgresVersion: "17.11", PgvectorVersion: "0.8.6", SQLiteVersion: "",
		GPUNote: "占有ベンチではない",
	}

	inputs := testReportInputs()

	measurement := eval.Measurement{
		Conditions: eval.Conditions{
			OrgID: 1, Alpha: 0.7, AlphaNote: "not tuned", Limit: 10, Rounds: 5,
			WarmupRounds: 1, KValues: eval.KValues(),
			GoldLengthThresholdRunes: eval.GoldLengthThreshold,
			LongChunkKeys:            eval.LongGoldKeys(),
			Ranking:                  testReportRanking(),
			PercentileMethod:         eval.PercentileMethod,
		},
		Queries: nil,
		Summary: eval.Summary{
			QueryCount: 0, Recall: nil, MRR: 0,
			Latency: eval.LatencySummary{
				WithEmbedding:    eval.LatencyStats{Samples: 0, MinMS: 0, P50MS: 0, P95MS: 0, MaxMS: 0},
				WithoutEmbedding: eval.LatencyStats{Samples: 0, MinMS: 0, P50MS: 0, P95MS: 0, MaxMS: 0},
			},
			TagRecall:        nil,
			MicroRecall:      eval.MicroRecall{Hits: 0, Total: 0, Cutoff: 10, Value: 0},
			GoldLengthRecall: nil,
			LongChunkRecall: eval.LongChunkRecall{
				Keys: nil, Hits: 0, Total: 0, Value: 0,
			},
		},
	}

	got := eval.NewReport(env, inputs, measurement, measuredAt)

	if got.Schema != eval.ReportSchema {
		t.Errorf("Schema = %q, want %q", got.Schema, eval.ReportSchema)
	}

	if got.MeasuredAt != "2026-09-01T12:00:00Z" {
		t.Errorf("MeasuredAt = %q", got.MeasuredAt)
	}

	if got.Environment.ModelDigest != env.ModelDigest {
		t.Errorf("ModelDigest = %q", got.Environment.ModelDigest)
	}

	if got.Inputs.Corpus.SHA256 != "aa" {
		t.Errorf("Corpus.SHA256 = %q", got.Inputs.Corpus.SHA256)
	}

	if got.Conditions.Alpha != 0.7 {
		t.Errorf("Alpha = %v", got.Conditions.Alpha)
	}
}

// TestReportMarshalsToJSON は、レポートがそのまま JSON として書き出せて、
// 検証に要る項目が名前付きで出ることを見る。
//
// docs/benchmarks/data/ にコミットして後から読み返すものなので、
// 項目名が消えたり変わったりしたら気づける状態にしておく。
func TestReportMarshalsToJSON(t *testing.T) {
	report := eval.NewReport(
		eval.Environment{
			GitRevision: "abc", GitModified: true, GoVersion: "go1.27.0",
			EmbedderID: "bge-m3:1024", OllamaVersion: "0.33.2", ModelDigest: "digest",
			PostgresVersion: "17.11", PgvectorVersion: "0.8.6", SQLiteVersion: "",
			GPUNote: "",
		},
		eval.Inputs{
			Corpus:  eval.FileInput{Path: "c", SHA256: "1", Count: 1},
			Queries: eval.FileInput{Path: "q", SHA256: "2", Count: 1},
			Tags:    eval.FileInput{Path: "t", SHA256: "3", Count: 1},
		},
		jsonTestMeasurement(),
		time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	)

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// 圏外の順位は null で残る。0 に潰れると1位と区別がつかない。
	want := []string{
		`"schema"`, `"measured_at"`, `"git_revision"`, `"model_digest"`,
		`"ollama_version"`, `"pgvector_version"`, `"sha256"`, `"alpha_note"`,
		`"percentile_method"`, `"ranked_keys"`, `"relevant_ranks"`, `"rank":null`,
		`"with_embedding_ms"`, `"without_embedding_ms"`, `"tag_recall"`, `"p95_ms"`,
		// v2 で足した項目。名前が消えたら、コーディネーターの集計が静かに
		// 欠けた数字を読むことになる。
		`"vector_score"`, `"lexical_score"`, `"micro_recall"`,
		`"gold_length_recall"`, `"long_chunk_recall"`,
		`"gold_length_threshold_runes"`, `"long_chunk_keys"`,
		// 条件の記録。alpha だけでは条件が決まらない。
		`"ranking"`, `"fusion"`, `"ts_rank_normalization"`, `"rrf_k"`,
	}

	for _, key := range want {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("JSON に %s が無い", key)
		}
	}
}

// TestRankingSettingsOmitTheKnobsAStoreDoesNotHave は、ストアに存在しない
// つまみが JSON から**キーごと**消え、実際に使っている 0 は残ることを見る。
//
// 🔴 これが様式 v4 の中心である。v3 までは sqlite のレポートにも
// ts_rank_normalization: 0 が入っており、SQLite に ts_rank は無いのに
// 「フラグ 0 で測った」と読めた。同時に postgres の 0 は消してはならない——
// int の omitempty では両方消えるので、ポインタで区別している。
func TestRankingSettingsOmitTheKnobsAStoreDoesNotHave(t *testing.T) {
	cases := map[string]struct {
		ranking eval.RankingSettings
		want    []string
		notWant []string
	}{
		"postgres は 0 のフラグを残す": {
			ranking: testReportRanking(),
			want: []string{
				`"store": "postgres"`, `"lexical_scorer": "ts_rank"`,
				`"ts_rank_normalization": 0`, `"rrf_k": 60`,
			},
			notWant: nil,
		},
		"sqlite は postgres 専用の項目を出さない": {
			ranking: testSQLiteRanking(),
			want: []string{
				`"store": "sqlite"`, `"lexical_scorer": "fts5-bm25"`,
			},
			notWant: []string{`"ts_rank_normalization"`, `"rrf_k"`},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.MarshalIndent(tc.ranking, "", "  ")
			if err != nil {
				t.Fatalf("json.MarshalIndent: %v", err)
			}

			assertJSONHas(t, string(encoded), tc.want)
			assertJSONLacks(t, string(encoded), tc.notWant)
		})
	}
}

// assertJSONHas は挙げた断片がすべて出ていることを見る。
func assertJSONHas(t *testing.T, encoded string, keys []string) {
	t.Helper()

	for _, key := range keys {
		if !strings.Contains(encoded, key) {
			t.Errorf("JSON に %s が無い:\n%s", key, encoded)
		}
	}
}

// assertJSONLacks は挙げた断片が1つも出ていないことを見る。
//
// 🔑 「出ていないこと」を見る側がこの修正の本題である。測っていない条件が
// レポートに載ると、読み手はそれを測ったものとして扱う。
func assertJSONLacks(t *testing.T, encoded string, keys []string) {
	t.Helper()

	for _, key := range keys {
		if strings.Contains(encoded, key) {
			t.Errorf("JSON に %s が出ている。測っていない条件を書かないこと:\n%s", key, encoded)
		}
	}
}

// TestAlphaIsRecordedAsTheDecimalThatWasGiven は、alpha が float32 の丸めを
// 帯びずに JSON へ出ることを見る。
//
// 🔴 v3 までは conditions.alpha が float32 だったので、0.6 が
// "alpha": 0.6000000238418579 と刻まれ、レポートを機械で突き合わせる側で
// == 0.6 が偽になった。数字を読み違える経路であって、丸めの美観の話ではない。
func TestAlphaIsRecordedAsTheDecimalThatWasGiven(t *testing.T) {
	for _, alpha := range []float64{0.6, 0.85, 0} {
		t.Run(strconv.FormatFloat(alpha, 'f', -1, 64), func(t *testing.T) {
			conditions := jsonTestMeasurement().Conditions
			conditions.Alpha = alpha

			encoded, err := json.Marshal(conditions)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}

			want := `"alpha":` + strconv.FormatFloat(alpha, 'f', -1, 64)
			if !strings.Contains(string(encoded), want) {
				t.Errorf("JSON に %s が無い。float32 の丸めが載っている:\n%s", want, encoded)
			}
		})
	}
}

// jsonTestMeasurement は JSON 書き出しの検査に使う計測結果を返す。
//
// テスト本体から切り出してあるのは、レポートの様式が増えるたびにリテラルが
// 伸びて、検査の本題（項目名が消えていないか）が読めなくなるためである。
func jsonTestMeasurement() eval.Measurement {
	return eval.Measurement{
		Conditions: eval.Conditions{
			OrgID: 1, Alpha: 0.7, AlphaNote: "not tuned", Limit: 10, Rounds: 5,
			WarmupRounds: 1, KValues: eval.KValues(),
			GoldLengthThresholdRunes: eval.GoldLengthThreshold,
			LongChunkKeys:            eval.LongGoldKeys(),
			Ranking:                  testReportRanking(),
			PercentileMethod:         eval.PercentileMethod,
		},
		Queries: []eval.QueryReport{{
			QueryID: "q-1", Text: "問い", Tags: []string{"語彙一致"},
			Relevant: []string{"doc-a#001"},
			RankedKeys: []eval.RankedEntry{{
				Key: "doc-a#001", Score: 0.7, VectorScore: 1, LexicalScore: 0,
			}},
			RelevantRanks:  []eval.RelevantRank{{Key: "doc-a#001", Rank: nil}},
			Recall:         []eval.RecallAtK{{K: 10, Value: 1}},
			ReciprocalRank: 1,
			Latencies: []eval.RoundLatency{
				{Round: 1, WithEmbeddingMS: 1.5, WithoutEmbeddingMS: 0.5},
			},
		}},
		Summary: eval.Summary{
			QueryCount: 1, Recall: []eval.RecallAtK{{K: 10, Value: 1}}, MRR: 1,
			Latency: eval.LatencySummary{
				WithEmbedding:    eval.LatencyStats{Samples: 1, MinMS: 1.5, P50MS: 1.5, P95MS: 1.5, MaxMS: 1.5},
				WithoutEmbedding: eval.LatencyStats{Samples: 1, MinMS: 0.5, P50MS: 0.5, P95MS: 0.5, MaxMS: 0.5},
			},
			TagRecall: []eval.TagRecall{{
				Tag: "語彙一致", QueryCount: 1, Recall: []eval.RecallAtK{{K: 10, Value: 1}},
			}},
			MicroRecall: eval.MicroRecall{Hits: 1, Total: 1, Cutoff: 10, Value: 1},
			GoldLengthRecall: []eval.GoldLengthBucket{{
				Label: "<=520", MinRunes: 0, MaxRunes: eval.GoldLengthThreshold,
				Hits: 1, Total: 1, Value: 1,
			}},
			LongChunkRecall: eval.LongChunkRecall{
				Keys: nil, Hits: 0, Total: 0, Value: 0,
			},
		},
	}
}

// testReportRanking はレポートの検査に使う順位付け条件の記録（postgres 形）。
//
// internal/eval はこの中身を解釈しない。JSON に項目が出ることだけが要求である。
//
// 🔴 TsRankNormalization は 0 を**指す**。postgres では 0 が実際に使っている
// 値なので、「無い」ではなく「0 で測った」として記録されなければならない。
func testReportRanking() eval.RankingSettings {
	return eval.RankingSettings{
		Fusion: "weighted-sum", Store: "postgres", LexicalScorer: "ts_rank",
		TokenizerID:         "bigram:nfkc-lower:v1",
		TsRankNormalization: intPtr(0), RRFK: intPtr(60),
	}
}

// testSQLiteRanking は sqlite 形の記録。postgres 専用の2項目を持たない。
func testSQLiteRanking() eval.RankingSettings {
	return eval.RankingSettings{
		Fusion: "weighted-sum", Store: "sqlite", LexicalScorer: "fts5-bm25",
		TokenizerID:         "bigram:nfkc-lower:v1",
		TsRankNormalization: nil, RRFK: nil,
	}
}

// intPtr は「値がある」ことを表すポインタを作る。
//
// ポインタなのは nil（そのストアにそのつまみが無い）と 0（0 で測った）を
// 区別するためなので、テスト側にも同じ区別が要る。
func intPtr(v int) *int { return &v }

// testReportInputs はレポートの検査に使う入力の同一性。
func testReportInputs() eval.Inputs {
	return eval.Inputs{
		Corpus:  eval.FileInput{Path: "testdata/eval/corpus.jsonl", SHA256: "aa", Count: 3},
		Queries: eval.FileInput{Path: "testdata/eval/queries.jsonl", SHA256: "bb", Count: 2},
		Tags:    eval.FileInput{Path: "testdata/eval/tags.json", SHA256: "cc", Count: 2},
	}
}

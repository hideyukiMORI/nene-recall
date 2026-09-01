package postgres

// 本ファイルはテストのためだけの公開窓である。
//
// テストは外部テストパッケージから公開 API 越しに書く（QLT-006）が、
// ここで検査したいのは境界の符号化と契約検査という未公開の部品である。
// 本体側で export すると、他パッケージから使える経路が「テストの都合」で
// 増えてしまうので、公開を _test.go に閉じる（GO-008）。

// VectorDimensions は vectorDimensions をテストへ公開する。
const VectorDimensions = vectorDimensions

// NormToleranceSquared は normToleranceSquared をテストへ公開する。
const NormToleranceSquared = normToleranceSquared

// EncodeVector は encodeVector をテストへ公開する。
func EncodeVector(v []float32) string { return encodeVector(v) }

// EncodeInt64Array は encodeInt64Array をテストへ公開する。
func EncodeInt64Array(ids []int64) string { return encodeInt64Array(ids) }

// ValidateVector は validateVector をテストへ公開する。
func ValidateVector(v []float32) error { return validateVector(v) }

// ErrVectorInvalid は errVectorInvalid をテストへ公開する。
func ErrVectorInvalid() error { return errVectorInvalid }

// ErrVectorDimensions は errVectorDimensions をテストへ公開する。
func ErrVectorDimensions() error { return errVectorDimensions }

// ErrVectorNotNormalized は errVectorNotNormalized をテストへ公開する。
func ErrVectorNotNormalized() error { return errVectorNotNormalized }

// ErrEmbedderDimensions は errEmbedderDimensions をテストへ公開する。
func ErrEmbedderDimensions() error { return errEmbedderDimensions }

// ErrEmbedderID は errEmbedderID をテストへ公開する。
func ErrEmbedderID() error { return errEmbedderID }

// ErrEmptyBatch は errEmptyBatch をテストへ公開する。
func ErrEmptyBatch() error { return errEmptyBatch }

// ErrEmptyContent は errEmptyContent をテストへ公開する。
func ErrEmptyContent() error { return errEmptyContent }

// ErrOrgMismatch は errOrgMismatch をテストへ公開する。
func ErrOrgMismatch() error { return errOrgMismatch }

// ErrChunkIDNotAccepted は errChunkIDNotAccepted をテストへ公開する。
func ErrChunkIDNotAccepted() error { return errChunkIDNotAccepted }

// ErrOrgRequired は errOrgRequired をテストへ公開する。
func ErrOrgRequired() error { return errOrgRequired }

// ErrTokenizerID は errTokenizerID をテストへ公開する。
func ErrTokenizerID() error { return errTokenizerID }

// ErrTokenInvalid は errTokenInvalid をテストへ公開する。
func ErrTokenInvalid() error { return errTokenInvalid }

// ErrTokenHasWhitespace は errTokenHasWhitespace をテストへ公開する。
func ErrTokenHasWhitespace() error { return errTokenHasWhitespace }

// ErrTokenHasMetaCharacter は errTokenHasMetaCharacter をテストへ公開する。
func ErrTokenHasMetaCharacter() error { return errTokenHasMetaCharacter }

// EncodeLexemeText は encodeLexemeText をテストへ公開する。
func EncodeLexemeText(tokens []string) (string, error) { return encodeLexemeText(tokens) }

// EncodeTsQuery は encodeTsQuery をテストへ公開する。
func EncodeTsQuery(tokens []string) (string, error) { return encodeTsQuery(tokens) }

// ErrUnknownFusion は errUnknownFusion をテストへ公開する。
func ErrUnknownFusion() error { return errUnknownFusion }

// StatementFor は Fusion.statement をテストへ公開する。
//
// 未知の方式に対する番人が働くことを、DB を立てずに確かめるために要る。
func StatementFor(f Fusion, alpha float32) (string, any, error) { return f.statement(alpha) }

package sqlite

// 本ファイルはテストのためだけの公開窓である。
//
// テストは外部テストパッケージから公開 API 越しに書く（QLT-006）が、
// ここで検査したいのは境界の符号化と契約検査という未公開の部品である。
// 本体側で export すると、他パッケージから使える経路が「テストの都合」で
// 増えてしまうので、公開を _test.go に閉じる（GO-008）。

// VectorDimensions は vectorDimensions をテストへ公開する。
const VectorDimensions = vectorDimensions

// VectorBytes は vectorBytes をテストへ公開する。
const VectorBytes = vectorBytes

// NormToleranceSquared は normToleranceSquared をテストへ公開する。
const NormToleranceSquared = normToleranceSquared

// EncodeVector は encodeVector をテストへ公開する。
func EncodeVector(v []float32) []byte { return encodeVector(v) }

// DecodeVector は decodeVector をテストへ公開する。
func DecodeVector(blob []byte) ([]float32, error) { return decodeVector(blob) }

// IDFilterJSON は idFilterJSON をテストへ公開する。
func IDFilterJSON(ids []int64) string { return idFilterJSON(ids) }

// ValidateVector は validateVector をテストへ公開する。
func ValidateVector(v []float32) error { return validateVector(v) }

// EncodeLexemeText は encodeLexemeText をテストへ公開する。
func EncodeLexemeText(tokens []string) (string, error) { return encodeLexemeText(tokens) }

// EncodeMatchExpression は encodeMatchExpression をテストへ公開する。
func EncodeMatchExpression(tokens []string) (string, error) { return encodeMatchExpression(tokens) }

// ErrVectorInvalid は errVectorInvalid をテストへ公開する。
func ErrVectorInvalid() error { return errVectorInvalid }

// ErrVectorDimensions は errVectorDimensions をテストへ公開する。
func ErrVectorDimensions() error { return errVectorDimensions }

// ErrVectorNotNormalized は errVectorNotNormalized をテストへ公開する。
func ErrVectorNotNormalized() error { return errVectorNotNormalized }

// ErrVectorBlobLength は errVectorBlobLength をテストへ公開する。
func ErrVectorBlobLength() error { return errVectorBlobLength }

// ErrEmbedderDimensions は errEmbedderDimensions をテストへ公開する。
func ErrEmbedderDimensions() error { return errEmbedderDimensions }

// ErrEmbedderID は errEmbedderID をテストへ公開する。
func ErrEmbedderID() error { return errEmbedderID }

// ErrTokenizerID は errTokenizerID をテストへ公開する。
func ErrTokenizerID() error { return errTokenizerID }

// ErrTokenInvalid は errTokenInvalid をテストへ公開する。
func ErrTokenInvalid() error { return errTokenInvalid }

// ErrTokenHasWhitespace は errTokenHasWhitespace をテストへ公開する。
func ErrTokenHasWhitespace() error { return errTokenHasWhitespace }

// ErrTokenHasMetaCharacter は errTokenHasMetaCharacter をテストへ公開する。
func ErrTokenHasMetaCharacter() error { return errTokenHasMetaCharacter }

// ErrEmptyBatch は errEmptyBatch をテストへ公開する。
func ErrEmptyBatch() error { return errEmptyBatch }

// ErrEmptyContent は errEmptyContent をテストへ公開する。
func ErrEmptyContent() error { return errEmptyContent }

// ErrOrgRequired は errOrgRequired をテストへ公開する。
func ErrOrgRequired() error { return errOrgRequired }

// ErrOrgMismatch は errOrgMismatch をテストへ公開する。
func ErrOrgMismatch() error { return errOrgMismatch }

// ErrChunkIDNotAccepted は errChunkIDNotAccepted をテストへ公開する。
func ErrChunkIDNotAccepted() error { return errChunkIDNotAccepted }

// ErrSearch は errSearch をテストへ公開する。
func ErrSearch() error { return errSearch }

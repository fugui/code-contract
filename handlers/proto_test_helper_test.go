package handlers_test

import (
	"context"
	"testing"

	"code-contract/handlers"
	"code-contract/internal/server/decoder"
	"code-contract/internal/server/protodoc"
	"code-contract/models"
	"gorm.io/gorm"
)

func compileTestProtoSchema(
	t *testing.T,
	db *gorm.DB,
	name string,
	category string,
	versionTag string,
	files map[string]string,
	autoActivate bool,
	allowBreaking bool,
	breakingReason string,
) (*models.ProtoSchema, []models.ProtoOperation, int, error) {
	t.Helper()
	reg, err := handlers.GetDecoderRegistry()
	if err != nil {
		return nil, nil, 0, err
	}
	schema, ops, _, err := reg.CompileAndSaveSchema(context.Background(), decoder.CompileProtoBundleInput{
		Name:           name,
		Category:       category,
		VersionTag:     versionTag,
		Files:          files,
		AutoActivate:   autoActivate,
		AllowBreaking:  allowBreaking,
		BreakingReason: breakingReason,
		SourceType:     models.ProtoSourceGitSync,
		OperatorID:     1,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	docCount, _ := protodoc.RebuildForSchema(db, schema)
	return schema, ops, docCount, nil
}

func uploadTestProtoBytes(
	t *testing.T,
	db *gorm.DB,
	name string,
	fileName string,
	category string,
	rawBytes []byte,
	autoActivate bool,
	allowBreaking bool,
	breakingReason string,
) (*models.ProtoSchema, []models.ProtoOperation, int, error) {
	t.Helper()
	reg, err := handlers.GetDecoderRegistry()
	if err != nil {
		return nil, nil, 0, err
	}
	if category == "" {
		category = models.ProtoCategoryBusiness
	}
	schema, ops, _, err := reg.UploadSchema(decoder.UploadSchemaInput{
		Name:           name,
		Category:       category,
		FileName:       fileName,
		RawBytes:       rawBytes,
		AutoActivate:   autoActivate,
		AllowBreaking:  allowBreaking,
		BreakingReason: breakingReason,
		SourceType:     models.ProtoSourceGitSync,
		OperatorID:     1,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	docCount, _ := protodoc.RebuildForSchema(db, schema)
	return schema, ops, docCount, nil
}

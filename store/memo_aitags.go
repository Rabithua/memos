package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/rabithua/memos/api"
)

type memoAiTagsRaw struct {
	MemoID int
	Tags   string
}

func (raw *memoAiTagsRaw) toMemoAitags() *api.MemoAiTags {
	return &api.MemoAiTags{
		MemoID: raw.MemoID,
		Tags:   raw.Tags,
	}
}

func (s *Store) UpsertMemoAiTags(ctx context.Context, upsert *api.MemoAiTagsUpsert) (*api.MemoAiTags, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Println(FormatError(err))
	}

	memoAiTagsRaw, err := upsertMemoAiTags(ctx, tx, upsert)
	if err != nil {
		return nil, err
	}

	defer tx.Rollback()

	if err := tx.Commit(); err != nil {
		return nil, FormatError(err)
	}

	return memoAiTagsRaw.toMemoAitags(), nil
}

func upsertMemoAiTags(ctx context.Context, tx *sql.Tx, upsert *api.MemoAiTagsUpsert) (*memoAiTagsRaw, error) {
	set := []string{"memo_id", "tags"}
	args := []any{upsert.MemoID, upsert.Tags}
	placeholder := []string{"?", "?"}

	query := `
		INSERT INTO memo_aitags (
			` + strings.Join(set, ", ") + `
		)
		VALUES (` + strings.Join(placeholder, ",") + `)
		ON CONFLICT(memo_id) DO UPDATE 
		SET
			tags = EXCLUDED.tags
		RETURNING memo_id, tags
	`
	var memoAiTagsRaw memoAiTagsRaw
	if err := tx.QueryRowContext(ctx, query, args...).Scan(
		&memoAiTagsRaw.MemoID,
		&memoAiTagsRaw.Tags,
	); err != nil {
		return nil, FormatError(err)
	}

	return &memoAiTagsRaw, nil
}

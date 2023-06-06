package api

type AiTags string

type MemoAiTagsUpsert struct {
	MemoID int `json:"-"`
	Tags   string
}

type MemoAiTags struct {
	MemoID    int
	Tags      string
	CreatedTs int64
	UpdatedTs int64
}

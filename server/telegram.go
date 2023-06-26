package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strconv"
	"time"

	"github.com/rabithua/memos/api"
	"github.com/rabithua/memos/common"
	"github.com/rabithua/memos/plugin/telegram"
	"github.com/rabithua/memos/store"
)

type telegramHandler struct {
	store *store.Store
}

func newTelegramHandler(store *store.Store) *telegramHandler {
	return &telegramHandler{store: store}
}

func (t *telegramHandler) RobotToken(ctx context.Context) string {
	return t.store.GetSystemSettingValueOrDefault(&ctx, api.SystemSettingTelegramRobotTokenName, "")
}

func (t *telegramHandler) MessageHandle(ctx context.Context, message telegram.Message, blobs map[string][]byte) error {
	var creatorID int
	userSettingList, err := t.store.FindUserSettingList(ctx, &api.UserSettingFind{
		Key: api.UserSettingTelegramUserIDKey,
	})
	if err != nil {
		return fmt.Errorf("Fail to find memo user: %s", err)
	}
	for _, userSetting := range userSettingList {
		var value string
		if err := json.Unmarshal([]byte(userSetting.Value), &value); err != nil {
			continue
		}

		if value == strconv.Itoa(message.From.ID) {
			creatorID = userSetting.UserID
		}
	}

	if creatorID == 0 {
		return fmt.Errorf("Please set your telegram userid %d in UserSetting of Memos", message.From.ID)
	}

	// create memo
	memoCreate := api.CreateMemoRequest{
		CreatorID:  creatorID,
		Visibility: api.Private,
	}

	if message.Text != nil {
		memoCreate.Content = *message.Text
	}
	if blobs != nil && message.Caption != nil {
		memoCreate.Content = *message.Caption
	}

	memoMessage, err := t.store.CreateMemo(ctx, convertCreateMemoRequestToMemoMessage(&memoCreate))
	if err != nil {
		return fmt.Errorf("failed to CreateMemo: %s", err)
	}

	if err := createMemoCreateActivity(ctx, t.store, memoMessage); err != nil {
		return fmt.Errorf("failed to createMemoCreateActivity: %s", err)
	}

	// 启动一个 Goroutine 执行异步函数
	go func() {
		// 创建一个新的 ctx，并使用 cancel 函数取消该 ctx
		ctx1, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		Aitags := getAiTags(&memoMessage.Content)
		fmt.Println(memoMessage.ID, Aitags)
		// 保存aitags
		if _, err := t.store.UpsertMemoAiTags(ctx1, &api.MemoAiTagsUpsert{
			MemoID: memoMessage.ID,
			Tags:   Aitags,
		}); err != nil {
			log.Printf("Failed to upsert memo resource: %v", err)
		}

		// 通知异步函数执行完毕
		// ch <- struct{}{}
	}()

	// create resources
	for filename, blob := range blobs {
		// TODO support more
		mime := "application/octet-stream"
		switch path.Ext(filename) {
		case ".jpg":
			mime = "image/jpeg"
		case ".png":
			mime = "image/png"
		}
		resourceCreate := api.ResourceCreate{
			CreatorID: creatorID,
			Filename:  filename,
			Type:      mime,
			Size:      int64(len(blob)),
			Blob:      blob,
			PublicID:  common.GenUUID(),
		}
		resource, err := t.store.CreateResource(ctx, &resourceCreate)
		if err != nil {
			return fmt.Errorf("failed to CreateResource: %s", err)
		}
		if err := createResourceCreateActivity(ctx, t.store, resource); err != nil {
			return fmt.Errorf("failed to createResourceCreateActivity: %s", err)
		}

		_, err = t.store.UpsertMemoResource(ctx, &api.MemoResourceUpsert{
			MemoID:     memoMessage.ID,
			ResourceID: resource.ID,
		})
		if err != nil {
			return fmt.Errorf("failed to UpsertMemoResource: %s", err)
		}
	}
	return nil
}

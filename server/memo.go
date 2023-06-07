package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/rabithua/memos/api"
	"github.com/rabithua/memos/common"
	"github.com/rabithua/memos/store"

	"github.com/labstack/echo/v4"
)

// maxContentLength means the max memo content bytes is 1MB.
const maxContentLength = 1 << 30

func getAiTags(content *string) string {

	if len(*content) == 0 {
		return "[]"
	}

	url := "https://www.wowow.club/api/chatgpt"
	method := "POST"

	payload := strings.NewReader(fmt.Sprintf(`{
        "content": "%s"
    }`, *content))

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		fmt.Println(err)
		return "[]"
	}
	req.Header.Add("User-Agent", "Apifox/1.0.0 (https://www.apifox.cn)")
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return "[]"
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "[]"
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return "[]"
	}
	return string(body)
}

func (s *Server) registerMemoRoutes(g *echo.Group) {
	// 创建备忘录的 API 接口
	g.POST("/memo", func(c echo.Context) error {
		ctx := c.Request().Context()

		// 从请求中获取用户 ID
		userID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "Missing user in session")
		}

		// 解析备忘录请求
		createMemoRequest := &api.CreateMemoRequest{}
		if err := json.NewDecoder(c.Request().Body).Decode(createMemoRequest); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Malformatted post memo request").SetInternal(err)
		}

		if len(createMemoRequest.Content) > maxContentLength {
			return echo.NewHTTPError(http.StatusBadRequest, "Content size overflow, up to 1MB")
		}

		// 处理备忘录的可见性
		if createMemoRequest.Visibility == "" {
			// 从数据库中获取用户备忘录可见性设置
			userMemoVisibilitySetting, err := s.Store.FindUserSetting(ctx, &api.UserSettingFind{
				UserID: &userID,
				Key:    api.UserSettingMemoVisibilityKey,
			})
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user setting").SetInternal(err)
			}

			if userMemoVisibilitySetting != nil {
				memoVisibility := api.Private
				err := json.Unmarshal([]byte(userMemoVisibilitySetting.Value), &memoVisibility)
				if err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to unmarshal user setting value").SetInternal(err)
				}
				createMemoRequest.Visibility = memoVisibility
			} else {
				// Private 是默认备忘录可见性
				createMemoRequest.Visibility = api.Private
			}
		}

		// 检查系统设置以禁用公共备忘录
		disablePublicMemosSystemSetting, err := s.Store.FindSystemSetting(ctx, &api.SystemSettingFind{
			Name: api.SystemSettingDisablePublicMemosName,
		})
		if err != nil && common.ErrorCode(err) != common.NotFound {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find system setting").SetInternal(err)
		}
		if disablePublicMemosSystemSetting != nil {
			disablePublicMemos := false
			err = json.Unmarshal([]byte(disablePublicMemosSystemSetting.Value), &disablePublicMemos)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to unmarshal system setting").SetInternal(err)
			}
			if disablePublicMemos {
				// 检查用户角色，如果是普通用户，则强制将备忘录设置为私有
				user, err := s.Store.FindUser(ctx, &api.UserFind{
					ID: &userID,
				})
				if err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find user").SetInternal(err)
				}
				if user.Role == "USER" {
					createMemoRequest.Visibility = api.Private
				}
			}
		}

		// 设置备忘录创建者 ID
		createMemoRequest.CreatorID = userID

		// 创建备忘录
		memoMessage, err := s.Store.CreateMemo(ctx, convertCreateMemoRequestToMemoMessage(createMemoRequest))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create memo").SetInternal(err)
		}
		// 创建备忘录活动
		if err := createMemoCreateActivity(c.Request().Context(), s.Store, memoMessage); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to create activity").SetInternal(err)
		}

		// ch := make(chan struct{})

		// 启动一个 Goroutine 执行异步函数
		go func() {
			// 创建一个新的 ctx，并使用 cancel 函数取消该 ctx
			ctx1, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			Aitags := getAiTags(&memoMessage.Content)
			fmt.Println(memoMessage.ID, Aitags)
			// 保存aitags
			if _, err := s.Store.UpsertMemoAiTags(ctx1, &api.MemoAiTagsUpsert{
				MemoID: memoMessage.ID,
				Tags:   Aitags,
			}); err != nil {
				log.Printf("Failed to upsert memo resource: %v", err)
			}

			// 通知异步函数执行完毕
			// ch <- struct{}{}
		}()

		// 等待异步函数执行完毕
		// <-ch

		// 处理备忘录资源
		for _, resourceID := range createMemoRequest.ResourceIDList {
			if _, err := s.Store.UpsertMemoResource(ctx, &api.MemoResourceUpsert{
				MemoID:     memoMessage.ID,
				ResourceID: resourceID,
			}); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upsert memo resource").SetInternal(err)
			}
		}

		// 处理备忘录关系
		for _, memoRelationUpsert := range createMemoRequest.RelationList {
			if _, err := s.Store.UpsertMemoRelation(ctx, &store.MemoRelationMessage{
				MemoID:        memoMessage.ID,
				RelatedMemoID: memoRelationUpsert.RelatedMemoID,
				Type:          store.MemoRelationType(memoRelationUpsert.Type),
			}); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upsert memo relation").SetInternal(err)
			}
		}

		// 获取完整的备忘录信息
		memoMessage, err = s.Store.GetMemo(ctx, &store.FindMemoMessage{
			ID: &memoMessage.ID,
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo").SetInternal(err)
		}
		memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
		}
		return c.JSON(http.StatusOK, composeResponse(memoResponse))
	})

	g.PATCH("/memo/:memoId", func(c echo.Context) error {
		ctx := c.Request().Context()
		userID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "Missing user in session")
		}

		memoID, err := strconv.Atoi(c.Param("memoId"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("ID is not a number: %s", c.Param("memoId"))).SetInternal(err)
		}

		memoMessage, err := s.Store.GetMemo(ctx, &store.FindMemoMessage{
			ID: &memoID,
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find memo").SetInternal(err)
		}
		if memoMessage.CreatorID != userID {
			return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
		}

		currentTs := time.Now().Unix()
		patchMemoRequest := &api.PatchMemoRequest{
			ID:        memoID,
			UpdatedTs: &currentTs,
		}
		if err := json.NewDecoder(c.Request().Body).Decode(patchMemoRequest); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Malformatted patch memo request").SetInternal(err)
		}

		if patchMemoRequest.Content != nil && len(*patchMemoRequest.Content) > maxContentLength {
			return echo.NewHTTPError(http.StatusBadRequest, "Content size overflow, up to 1MB").SetInternal(err)
		}

		updateMemoMessage := &store.UpdateMemoMessage{
			ID:        memoID,
			CreatedTs: patchMemoRequest.CreatedTs,
			UpdatedTs: patchMemoRequest.UpdatedTs,
			Content:   patchMemoRequest.Content,
		}
		if patchMemoRequest.RowStatus != nil {
			rowStatus := store.RowStatus(patchMemoRequest.RowStatus.String())
			updateMemoMessage.RowStatus = &rowStatus
		}
		if patchMemoRequest.Visibility != nil {
			visibility := store.Visibility(patchMemoRequest.Visibility.String())
			updateMemoMessage.Visibility = &visibility
		}

		err = s.Store.UpdateMemo(ctx, updateMemoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to patch memo").SetInternal(err)
		}
		memoMessage, err = s.Store.GetMemo(ctx, &store.FindMemoMessage{ID: &memoID})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find memo").SetInternal(err)
		}

		if patchMemoRequest.ResourceIDList != nil {
			addedResourceIDList, removedResourceIDList := getIDListDiff(memoMessage.ResourceIDList, patchMemoRequest.ResourceIDList)
			for _, resourceID := range addedResourceIDList {
				if _, err := s.Store.UpsertMemoResource(ctx, &api.MemoResourceUpsert{
					MemoID:     memoMessage.ID,
					ResourceID: resourceID,
				}); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upsert memo resource").SetInternal(err)
				}
			}
			for _, resourceID := range removedResourceIDList {
				if err := s.Store.DeleteMemoResource(ctx, &api.MemoResourceDelete{
					MemoID:     &memoMessage.ID,
					ResourceID: &resourceID,
				}); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete memo resource").SetInternal(err)
				}
			}
		}

		go func() {
			// 创建一个新的 ctx，并使用 cancel 函数取消该 ctx
			ctx1, cancel := context.WithTimeout(context.Background(), time.Second*10)
			defer cancel()

			Aitags := getAiTags(&memoMessage.Content)
			fmt.Println(memoMessage.ID, Aitags)
			// 保存aitags
			if _, err := s.Store.UpsertMemoAiTags(ctx1, &api.MemoAiTagsUpsert{
				MemoID: memoMessage.ID,
				Tags:   Aitags,
			}); err != nil {
				log.Printf("Failed to upsert memo resource: %v", err)
			}

			// 通知异步函数执行完毕
			// ch <- struct{}{}
		}()

		if patchMemoRequest.RelationList != nil {
			patchMemoRelationList := make([]*store.MemoRelationMessage, 0)
			for _, memoRelation := range patchMemoRequest.RelationList {
				patchMemoRelationList = append(patchMemoRelationList, &store.MemoRelationMessage{
					MemoID:        memoMessage.ID,
					RelatedMemoID: memoRelation.RelatedMemoID,
					Type:          store.MemoRelationType(memoRelation.Type),
				})
			}
			addedMemoRelationList, removedMemoRelationList := getMemoRelationListDiff(memoMessage.RelationList, patchMemoRelationList)
			for _, memoRelation := range addedMemoRelationList {
				if _, err := s.Store.UpsertMemoRelation(ctx, memoRelation); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upsert memo relation").SetInternal(err)
				}
			}
			for _, memoRelation := range removedMemoRelationList {
				if err := s.Store.DeleteMemoRelation(ctx, &store.DeleteMemoRelationMessage{
					MemoID:        &memoMessage.ID,
					RelatedMemoID: &memoRelation.RelatedMemoID,
					Type:          &memoRelation.Type,
				}); err != nil {
					return echo.NewHTTPError(http.StatusInternalServerError, "Failed to delete memo relation").SetInternal(err)
				}
			}
		}

		memoMessage, err = s.Store.GetMemo(ctx, &store.FindMemoMessage{ID: &memoID})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find memo").SetInternal(err)
		}
		memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
		}
		return c.JSON(http.StatusOK, composeResponse(memoResponse))
	})

	// 定义 GET 请求路由 "/memo"
	g.GET("/memo", func(c echo.Context) error {
		// 获取请求的 Context
		ctx := c.Request().Context()

		// 创建查找备忘录信息的对象
		findMemoMessage := &store.FindMemoMessage{}

		// 如果请求参数中有 creatorId，将其加入查找信息对象中
		if userID, err := strconv.Atoi(c.QueryParam("creatorId")); err == nil {
			findMemoMessage.CreatorID = &userID
		}

		// 获取当前用户的 ID
		currentUserID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			// 如果没有当前用户，且没有指定 creatorId，则返回错误信息
			if findMemoMessage.CreatorID == nil {
				return echo.NewHTTPError(http.StatusBadRequest, "Missing user id to find memo")
			}
			// 否则，指定可见性为公开
			findMemoMessage.VisibilityList = []store.Visibility{store.Public}
		} else {
			// 如果有当前用户
			if findMemoMessage.CreatorID == nil {
				// 如果没有指定 creatorId，则将当前用户的 ID 加入查找信息对象中
				findMemoMessage.CreatorID = &currentUserID
			} else {
				// 否则，可见性包括公开和保护两种
				findMemoMessage.VisibilityList = []store.Visibility{store.Public, store.Protected}
			}
		}

		// 解析请求参数中的 rowStatus，加入查找信息对象中
		rowStatus := store.RowStatus(c.QueryParam("rowStatus"))
		if rowStatus != "" {
			findMemoMessage.RowStatus = &rowStatus
		}

		// 解析请求参数中的 pinned，加入查找信息对象中
		pinnedStr := c.QueryParam("pinned")
		if pinnedStr != "" {
			pinned := pinnedStr == "true"
			findMemoMessage.Pinned = &pinned
		}

		// 解析请求参数中的 tag 和 content，拼接成搜索关键字，加入查找信息对象中
		contentSearch := []string{}
		tag := c.QueryParam("tag")
		if tag != "" {
			contentSearch = append(contentSearch, "#"+tag)
		}
		contentSlice := c.QueryParams()["content"]
		if len(contentSlice) > 0 {
			contentSearch = append(contentSearch, contentSlice...)
		}
		findMemoMessage.ContentSearch = contentSearch

		// 解析请求参数中的 visibility，加入查找信息对象中
		visibilityListStr := c.QueryParam("visibility")
		if visibilityListStr != "" {
			visibilityList := []store.Visibility{}
			for _, visibility := range strings.Split(visibilityListStr, ",") {
				visibilityList = append(visibilityList, store.Visibility(visibility))
			}
			findMemoMessage.VisibilityList = visibilityList
		}

		// 解析请求参数中的 limit 和 offset，加入查找信息对象中
		if limit, err := strconv.Atoi(c.QueryParam("limit")); err == nil {
			findMemoMessage.Limit = &limit
		}
		if offset, err := strconv.Atoi(c.QueryParam("offset")); err == nil {
			findMemoMessage.Offset = &offset
		}

		// 获取备忘录是否显示更新时间的设置值，加入查找信息对象中
		memoDisplayWithUpdatedTs, err := s.getMemoDisplayWithUpdatedTsSettingValue(ctx)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get memo display with updated ts setting value").SetInternal(err)
		}
		if memoDisplayWithUpdatedTs {
			findMemoMessage.OrderByUpdatedTs = true
		}

		// 获取符合查找信息的备忘录列表
		memoMessageList, err := s.Store.ListMemos(ctx, findMemoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to fetch memo list").SetInternal(err)
		}

		// 将备忘录信息转化为响应对象
		memoResponseList := []*api.MemoResponse{}
		for _, memoMessage := range memoMessageList {
			memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
			}
			memoResponseList = append(memoResponseList, memoResponse)
		}

		// 返回响应
		return c.JSON(http.StatusOK, composeResponse(memoResponseList))
	})

	g.GET("/memo/:memoId", func(c echo.Context) error {
		ctx := c.Request().Context()
		memoID, err := strconv.Atoi(c.Param("memoId"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("ID is not a number: %s", c.Param("memoId"))).SetInternal(err)
		}

		memoMessage, err := s.Store.GetMemo(ctx, &store.FindMemoMessage{
			ID: &memoID,
		})
		if err != nil {
			if common.ErrorCode(err) == common.NotFound {
				return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Memo ID not found: %d", memoID)).SetInternal(err)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to find memo by ID: %v", memoID)).SetInternal(err)
		}

		userID, ok := c.Get(getUserIDContextKey()).(int)
		if memoMessage.Visibility == store.Private {
			if !ok || memoMessage.CreatorID != userID {
				return echo.NewHTTPError(http.StatusForbidden, "this memo is private only")
			}
		} else if memoMessage.Visibility == store.Protected {
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "this memo is protected, missing user in session")
			}
		}
		memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
		}
		return c.JSON(http.StatusOK, composeResponse(memoResponse))
	})

	g.POST("/memo/:memoId/organizer", func(c echo.Context) error {
		ctx := c.Request().Context()
		memoID, err := strconv.Atoi(c.Param("memoId"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("ID is not a number: %s", c.Param("memoId"))).SetInternal(err)
		}

		userID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "Missing user in session")
		}
		memoOrganizerUpsert := &api.MemoOrganizerUpsert{}
		if err := json.NewDecoder(c.Request().Body).Decode(memoOrganizerUpsert); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Malformatted post memo organizer request").SetInternal(err)
		}
		memoOrganizerUpsert.MemoID = memoID
		memoOrganizerUpsert.UserID = userID

		err = s.Store.UpsertMemoOrganizer(ctx, memoOrganizerUpsert)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upsert memo organizer").SetInternal(err)
		}

		memoMessage, err := s.Store.GetMemo(ctx, &store.FindMemoMessage{
			ID: &memoID,
		})
		if err != nil {
			if common.ErrorCode(err) == common.NotFound {
				return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Memo ID not found: %d", memoID)).SetInternal(err)
			}
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to find memo by ID: %v", memoID)).SetInternal(err)
		}
		memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
		}
		return c.JSON(http.StatusOK, composeResponse(memoResponse))
	})

	g.GET("/memo/stats", func(c echo.Context) error {
		ctx := c.Request().Context()
		normalStatus := store.Normal
		findMemoMessage := &store.FindMemoMessage{
			RowStatus: &normalStatus,
		}
		if creatorID, err := strconv.Atoi(c.QueryParam("creatorId")); err == nil {
			findMemoMessage.CreatorID = &creatorID
		}
		if findMemoMessage.CreatorID == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "Missing user id to find memo")
		}

		currentUserID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			findMemoMessage.VisibilityList = []store.Visibility{store.Public}
		} else {
			if *findMemoMessage.CreatorID != currentUserID {
				findMemoMessage.VisibilityList = []store.Visibility{store.Public, store.Protected}
			} else {
				findMemoMessage.VisibilityList = []store.Visibility{store.Public, store.Protected, store.Private}
			}
		}

		memoDisplayWithUpdatedTs, err := s.getMemoDisplayWithUpdatedTsSettingValue(ctx)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get memo display with updated ts setting value").SetInternal(err)
		}
		if memoDisplayWithUpdatedTs {
			findMemoMessage.OrderByUpdatedTs = true
		}

		memoMessageList, err := s.Store.ListMemos(ctx, findMemoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find memo list").SetInternal(err)
		}
		memoResponseList := []*api.MemoResponse{}
		for _, memoMessage := range memoMessageList {
			memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
			}
			memoResponseList = append(memoResponseList, memoResponse)
		}

		displayTsList := []int64{}
		for _, memo := range memoResponseList {
			displayTsList = append(displayTsList, memo.DisplayTs)
		}
		return c.JSON(http.StatusOK, composeResponse(displayTsList))
	})

	g.GET("/memo/all", func(c echo.Context) error {
		ctx := c.Request().Context()
		findMemoMessage := &store.FindMemoMessage{}
		_, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			findMemoMessage.VisibilityList = []store.Visibility{store.Public}
		} else {
			findMemoMessage.VisibilityList = []store.Visibility{store.Public, store.Protected}
		}

		pinnedStr := c.QueryParam("pinned")
		if pinnedStr != "" {
			pinned := pinnedStr == "true"
			findMemoMessage.Pinned = &pinned
		}

		contentSearch := []string{}
		tag := c.QueryParam("tag")
		if tag != "" {
			contentSearch = append(contentSearch, "#"+tag+" ")
		}
		contentSlice := c.QueryParams()["content"]
		if len(contentSlice) > 0 {
			contentSearch = append(contentSearch, contentSlice...)
		}
		findMemoMessage.ContentSearch = contentSearch

		visibilityListStr := c.QueryParam("visibility")
		if visibilityListStr != "" {
			visibilityList := []store.Visibility{}
			for _, visibility := range strings.Split(visibilityListStr, ",") {
				visibilityList = append(visibilityList, store.Visibility(visibility))
			}
			findMemoMessage.VisibilityList = visibilityList
		}
		if limit, err := strconv.Atoi(c.QueryParam("limit")); err == nil {
			findMemoMessage.Limit = &limit
		}
		if offset, err := strconv.Atoi(c.QueryParam("offset")); err == nil {
			findMemoMessage.Offset = &offset
		}

		// Only fetch normal status memos.
		normalStatus := store.Normal
		findMemoMessage.RowStatus = &normalStatus

		memoDisplayWithUpdatedTs, err := s.getMemoDisplayWithUpdatedTsSettingValue(ctx)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to get memo display with updated ts setting value").SetInternal(err)
		}
		if memoDisplayWithUpdatedTs {
			findMemoMessage.OrderByUpdatedTs = true
		}

		memoMessageList, err := s.Store.ListMemos(ctx, findMemoMessage)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to fetch all memo list").SetInternal(err)
		}
		memoResponseList := []*api.MemoResponse{}
		for _, memoMessage := range memoMessageList {
			memoResponse, err := s.composeMemoMessageToMemoResponse(ctx, memoMessage)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "Failed to compose memo response").SetInternal(err)
			}
			memoResponseList = append(memoResponseList, memoResponse)
		}
		return c.JSON(http.StatusOK, composeResponse(memoResponseList))
	})

	g.DELETE("/memo/:memoId", func(c echo.Context) error {
		ctx := c.Request().Context()
		userID, ok := c.Get(getUserIDContextKey()).(int)
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "Missing user in session")
		}
		memoID, err := strconv.Atoi(c.Param("memoId"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("ID is not a number: %s", c.Param("memoId"))).SetInternal(err)
		}

		memo, err := s.Store.GetMemo(ctx, &store.FindMemoMessage{
			ID: &memoID,
		})
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to find memo").SetInternal(err)
		}
		if memo.CreatorID != userID {
			return echo.NewHTTPError(http.StatusUnauthorized, "Unauthorized")
		}

		if err := s.Store.DeleteMemo(ctx, &store.DeleteMemoMessage{
			ID: memoID,
		}); err != nil {
			if common.ErrorCode(err) == common.NotFound {
				return echo.NewHTTPError(http.StatusNotFound, fmt.Sprintf("Memo ID not found: %d", memoID))
			}
			return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to delete memo ID: %v", memoID)).SetInternal(err)
		}
		return c.JSON(http.StatusOK, true)
	})
}

func createMemoCreateActivity(ctx context.Context, store *store.Store, memo *store.MemoMessage) error {
	payload := api.ActivityMemoCreatePayload{
		Content:    memo.Content,
		Visibility: memo.Visibility.String(),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrap(err, "failed to marshal activity payload")
	}
	activity, err := store.CreateActivity(ctx, &api.ActivityCreate{
		CreatorID: memo.CreatorID,
		Type:      api.ActivityMemoCreate,
		Level:     api.ActivityInfo,
		Payload:   string(payloadBytes),
	})
	if err != nil || activity == nil {
		return errors.Wrap(err, "failed to create activity")
	}
	return err
}

func getIDListDiff(oldList, newList []int) (addedList, removedList []int) {
	oldMap := map[int]bool{}
	for _, id := range oldList {
		oldMap[id] = true
	}
	newMap := map[int]bool{}
	for _, id := range newList {
		newMap[id] = true
	}
	for id := range oldMap {
		if !newMap[id] {
			removedList = append(removedList, id)
		}
	}
	for id := range newMap {
		if !oldMap[id] {
			addedList = append(addedList, id)
		}
	}
	return addedList, removedList
}

func getMemoRelationListDiff(oldList, newList []*store.MemoRelationMessage) (addedList, removedList []*store.MemoRelationMessage) {
	oldMap := map[string]bool{}
	for _, relation := range oldList {
		oldMap[fmt.Sprintf("%d-%s", relation.RelatedMemoID, relation.Type)] = true
	}
	newMap := map[string]bool{}
	for _, relation := range newList {
		newMap[fmt.Sprintf("%d-%s", relation.RelatedMemoID, relation.Type)] = true
	}
	for _, relation := range oldList {
		key := fmt.Sprintf("%d-%s", relation.RelatedMemoID, relation.Type)
		if !newMap[key] {
			removedList = append(removedList, relation)
		}
	}
	for _, relation := range newList {
		key := fmt.Sprintf("%d-%s", relation.RelatedMemoID, relation.Type)
		if !oldMap[key] {
			addedList = append(addedList, relation)
		}
	}
	return addedList, removedList
}

func convertCreateMemoRequestToMemoMessage(memoCreate *api.CreateMemoRequest) *store.MemoMessage {
	createdTs := time.Now().Unix()
	if memoCreate.CreatedTs != nil {
		createdTs = *memoCreate.CreatedTs
	}
	return &store.MemoMessage{
		CreatorID:  memoCreate.CreatorID,
		CreatedTs:  createdTs,
		Content:    memoCreate.Content,
		Visibility: store.Visibility(memoCreate.Visibility),
	}
}

func (s *Server) composeMemoMessageToMemoResponse(ctx context.Context, memoMessage *store.MemoMessage) (*api.MemoResponse, error) {
	memoResponse := &api.MemoResponse{
		ID:         memoMessage.ID,
		RowStatus:  api.RowStatus(memoMessage.RowStatus.String()),
		CreatorID:  memoMessage.CreatorID,
		CreatedTs:  memoMessage.CreatedTs,
		UpdatedTs:  memoMessage.UpdatedTs,
		Content:    memoMessage.Content,
		Visibility: api.Visibility(memoMessage.Visibility.String()),
		Pinned:     memoMessage.Pinned,
	}

	// Compose creator name.
	user, err := s.Store.FindUser(ctx, &api.UserFind{
		ID: &memoResponse.CreatorID,
	})
	if err != nil {
		return nil, err
	}
	if user.Nickname != "" {
		memoResponse.CreatorName = user.Nickname
	} else {
		memoResponse.CreatorName = user.Username
	}

	// Compose display ts.
	memoResponse.DisplayTs = memoResponse.CreatedTs
	// Find memo display with updated ts setting.
	memoDisplayWithUpdatedTs, err := s.getMemoDisplayWithUpdatedTsSettingValue(ctx)
	if err != nil {
		return nil, err
	}
	if memoDisplayWithUpdatedTs {
		memoResponse.DisplayTs = memoResponse.UpdatedTs
	}

	relationList := []*api.MemoRelation{}
	for _, relation := range memoMessage.RelationList {
		relationList = append(relationList, convertMemoRelationMessageToMemoRelation(relation))
	}
	memoResponse.RelationList = relationList

	memoResponse.AiTags = memoMessage.AiTags

	resourceList := []*api.Resource{}
	for _, resourceID := range memoMessage.ResourceIDList {
		resource, err := s.Store.FindResource(ctx, &api.ResourceFind{
			ID: &resourceID,
		})
		if err != nil {
			return nil, err
		}
		resourceList = append(resourceList, resource)
	}
	memoResponse.ResourceList = resourceList

	return memoResponse, nil
}

func (s *Server) getMemoDisplayWithUpdatedTsSettingValue(ctx context.Context) (bool, error) {
	memoDisplayWithUpdatedTsSetting, err := s.Store.FindSystemSetting(ctx, &api.SystemSettingFind{
		Name: api.SystemSettingMemoDisplayWithUpdatedTsName,
	})
	if err != nil && common.ErrorCode(err) != common.NotFound {
		return false, errors.Wrap(err, "failed to find system setting")
	}
	memoDisplayWithUpdatedTs := false
	if memoDisplayWithUpdatedTsSetting != nil {
		err = json.Unmarshal([]byte(memoDisplayWithUpdatedTsSetting.Value), &memoDisplayWithUpdatedTs)
		if err != nil {
			return false, errors.Wrap(err, "failed to unmarshal system setting value")
		}
	}
	return memoDisplayWithUpdatedTs, nil
}

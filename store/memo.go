package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rabithua/memos/common"
)

// Visibility is the type of a visibility.
type Visibility string

const (
	// Public is the PUBLIC visibility.
	Public Visibility = "PUBLIC"
	// Protected is the PROTECTED visibility.
	Protected Visibility = "PROTECTED"
	// Private is the PRIVATE visibility.
	Private Visibility = "PRIVATE"
)

func (v Visibility) String() string {
	switch v {
	case Public:
		return "PUBLIC"
	case Protected:
		return "PROTECTED"
	case Private:
		return "PRIVATE"
	}
	return "PRIVATE"
}

type MemoMessage struct {
	ID int

	// Standard fields
	RowStatus RowStatus
	CreatorID int
	CreatedTs int64
	UpdatedTs int64

	// Domain specific fields
	Content    string
	Visibility Visibility
	AiTags     string

	// Composed fields
	Pinned         bool
	ResourceIDList []int
	RelationList   []*MemoRelationMessage
}

type FindMemoMessage struct {
	ID *int

	// Standard fields
	RowStatus *RowStatus
	CreatorID *int

	// Domain specific fields
	Pinned         *bool
	ContentSearch  []string
	VisibilityList []Visibility

	// Pagination
	Limit            *int
	Offset           *int
	OrderByUpdatedTs bool
}

type UpdateMemoMessage struct {
	ID         int
	CreatedTs  *int64
	UpdatedTs  *int64
	RowStatus  *RowStatus
	Content    *string
	Visibility *Visibility
}

type DeleteMemoMessage struct {
	ID int
}

func (s *Store) CreateMemo(ctx context.Context, create *MemoMessage) (*MemoMessage, error) {
	// 开始一个数据库事务
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, FormatError(err)
	}
	// 当函数结束时回滚事务
	defer tx.Rollback()

	// 如果创建时间为空，则设置为当前时间戳
	if create.CreatedTs == 0 {
		create.CreatedTs = time.Now().Unix()
	}

	create.AiTags = `tagName`

	// SQL 插入语句，返回插入的行的 id, created_ts, updated_ts, row_status 列的值
	query := `
		INSERT INTO memo (
			creator_id,
			created_ts,
			content,
			visibility,
			aitags
		)
		VALUES (?, ?, ?, ?, ?)
		RETURNING id, created_ts, updated_ts, row_status
	`
	if err := tx.QueryRowContext(
		ctx,
		query,
		create.CreatorID,
		create.CreatedTs,
		create.Content,
		create.Visibility,
		create.AiTags,
	).Scan(
		&create.ID, // 扫描查询结果返回的列
		&create.CreatedTs,
		&create.UpdatedTs,
		&create.RowStatus,
	); err != nil {
		return nil, FormatError(err)
	}
	// 提交事务
	if err := tx.Commit(); err != nil {
		return nil, FormatError(err)
	}
	// 返回插入的消息
	memoMessage := create
	return memoMessage, nil
}

// ListMemos 从数据库中获取符合条件的备忘录列表
// 参数ctx为上下文，find为查找备忘录的条件
// 函数返回符合条件的备忘录列表以及可能的错误
func (s *Store) ListMemos(ctx context.Context, find *FindMemoMessage) ([]*MemoMessage, error) {
	// 开启一个事务
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		fmt.Println(111)
		return nil, FormatError(err)
	}
	defer tx.Rollback()

	// 调用listMemos函数获取备忘录列表
	list, err := listMemos(ctx, tx, find)
	if err != nil {
		fmt.Println(nil, err)
		return nil, err
	}

	return list, nil
}

func (s *Store) GetMemo(ctx context.Context, find *FindMemoMessage) (*MemoMessage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, FormatError(err)
	}
	defer tx.Rollback()

	list, err := listMemos(ctx, tx, find)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, &common.Error{Code: common.NotFound, Err: fmt.Errorf("memo not found")}
	}

	memoMessage := list[0]
	return memoMessage, nil
}

func (s *Store) UpdateMemo(ctx context.Context, update *UpdateMemoMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	set, args := []string{}, []any{}
	if v := update.CreatedTs; v != nil {
		set, args = append(set, "created_ts = ?"), append(args, *v)
	}
	if v := update.UpdatedTs; v != nil {
		set, args = append(set, "updated_ts = ?"), append(args, *v)
	}
	if v := update.RowStatus; v != nil {
		set, args = append(set, "row_status = ?"), append(args, *v)
	}
	if v := update.Content; v != nil {
		set, args = append(set, "content = ?"), append(args, *v)
	}
	if v := update.Visibility; v != nil {
		set, args = append(set, "visibility = ?"), append(args, *v)
	}
	args = append(args, update.ID)

	query := `
		UPDATE memo
		SET ` + strings.Join(set, ", ") + `
		WHERE id = ?
	`
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

func (s *Store) DeleteMemo(ctx context.Context, delete *DeleteMemoMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FormatError(err)
	}
	defer tx.Rollback()

	where, args := []string{"id = ?"}, []any{delete.ID}
	stmt := `DELETE FROM memo WHERE ` + strings.Join(where, " AND ")
	result, err := tx.ExecContext(ctx, stmt, args...)
	if err != nil {
		return FormatError(err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return &common.Error{Code: common.NotFound, Err: fmt.Errorf("idp not found")}
	}
	if err := s.vacuumImpl(ctx, tx); err != nil {
		return err
	}
	err = tx.Commit()
	return err
}

// listMemos 根据查找条件从数据库中检索备忘录列表
// ctx 上下文环境
// tx 数据库事务
// find 查找备忘录的查询条件
// 返回符合条件的备忘录列表和可能的错误
func listMemos(ctx context.Context, tx *sql.Tx, find *FindMemoMessage) ([]*MemoMessage, error) {
	// 初始化查询条件和参数
	where, args := []string{"1 = 1"}, []any{}

	// 根据 ID 进行查找
	if v := find.ID; v != nil {
		where, args = append(where, "memo.id = ?"), append(args, *v)
	}
	// 根据创建者 ID 进行查找
	if v := find.CreatorID; v != nil {
		where, args = append(where, "memo.creator_id = ?"), append(args, *v)
	}
	// 根据行状态进行查找
	if v := find.RowStatus; v != nil {
		where, args = append(where, "memo.row_status = ?"), append(args, *v)
	}
	// 是否按置顶排序
	if v := find.Pinned; v != nil {
		where = append(where, "memo_organizer.pinned = 1")
	}
	// 根据内容关键词进行查找
	if v := find.ContentSearch; len(v) != 0 {
		for _, s := range v {
			where, args = append(where, "memo.content LIKE ?"), append(args, "%"+s+"%")
		}
	}
	// 根据可见性列表进行查找
	if v := find.VisibilityList; len(v) != 0 {
		list := []string{}
		for _, visibility := range v {
			list = append(list, fmt.Sprintf("$%d", len(args)+1))
			args = append(args, visibility)
		}
		where = append(where, fmt.Sprintf("memo.visibility in (%s)", strings.Join(list, ",")))
	}

	// 初始化排序条件
	orders := []string{"pinned DESC"}
	if find.OrderByUpdatedTs {
		orders = append(orders, "updated_ts DESC")
	} else {
		orders = append(orders, "created_ts DESC")
	}

	// 准备 SQL 查询语句
	query := `
	SELECT
		memo.id AS id,
		memo.creator_id AS creator_id,
		memo.created_ts AS created_ts,
		memo.updated_ts AS updated_ts,
		memo.row_status AS row_status,
		memo.content AS content,
		memo.visibility AS visibility,
		memo.aitags AS aitags,
		CASE WHEN memo_organizer.pinned = 1 THEN 1 ELSE 0 END AS pinned,
		GROUP_CONCAT(memo_resource.resource_id) AS resource_id_list,
		(
				SELECT
						GROUP_CONCAT(related_memo_id || ':' || type)
				FROM
						memo_relation
				WHERE
						memo_relation.memo_id = memo.id
				GROUP BY
						memo_relation.memo_id
		) AS relation_list
	FROM
		memo
	LEFT JOIN
		memo_organizer ON memo.id = memo_organizer.memo_id
	LEFT JOIN
		memo_resource ON memo.id = memo_resource.memo_id
	WHERE ` + strings.Join(where, " AND ") + `
	GROUP BY memo.id
	ORDER BY ` + strings.Join(orders, ", ") + `
	`
	// 限制查询结果数量
	if find.Limit != nil {
		query = fmt.Sprintf("%s LIMIT %d", query, *find.Limit)
		if find.Offset != nil {
			query = fmt.Sprintf("%s OFFSET %d", query, *find.Offset)
		}
	}

	// 执行 SQL 查询语句
	fmt.Println(query)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, FormatError(err)
	}
	defer rows.Close()

	// 解析查询结果
	memoMessageList := make([]*MemoMessage, 0)
	for rows.Next() {
		var memoMessage MemoMessage
		var memoResourceIDList sql.NullString
		var memoRelationList sql.NullString
		fmt.Printf("Type is %T\n", memoMessage.AiTags)

		if err := rows.Scan(
			&memoMessage.ID,
			&memoMessage.CreatorID,
			&memoMessage.CreatedTs,
			&memoMessage.UpdatedTs,
			&memoMessage.RowStatus,
			&memoMessage.Content,
			&memoMessage.Visibility,
			&memoMessage.AiTags,
			&memoMessage.Pinned,
			&memoResourceIDList,
			&memoRelationList,
		); err != nil {
			fmt.Println(333, err)
			return nil, FormatError(err)
		}

		// 解析备忘录中的资源 ID
		if memoResourceIDList.Valid {
			idStringList := strings.Split(memoResourceIDList.String, ",")
			memoMessage.ResourceIDList = make([]int, 0, len(idStringList))
			for _, idString := range idStringList {
				id, err := strconv.Atoi(idString)
				if err != nil {
					return nil, FormatError(err)
				}
				memoMessage.ResourceIDList = append(memoMessage.ResourceIDList, id)
			}
		}

		// 解析备忘录中的关系列表
		if memoRelationList.Valid {
			memoMessage.RelationList = make([]*MemoRelationMessage, 0)
			relatedMemoTypeList := strings.Split(memoRelationList.String, ",")
			for _, relatedMemoType := range relatedMemoTypeList {
				relatedMemoTypeList := strings.Split(relatedMemoType, ":")
				if len(relatedMemoTypeList) != 2 {
					return nil, &common.Error{Code: common.Invalid, Err: fmt.Errorf("invalid relation format")}
				}
				relatedMemoID, err := strconv.Atoi(relatedMemoTypeList[0])
				if err != nil {
					return nil, FormatError(err)
				}
				memoMessage.RelationList = append(memoMessage.RelationList, &MemoRelationMessage{
					MemoID:        memoMessage.ID,
					RelatedMemoID: relatedMemoID,
					Type:          MemoRelationType(relatedMemoTypeList[1]),
				})
			}
		}
		memoMessageList = append(memoMessageList, &memoMessage)
	}

	if err := rows.Err(); err != nil {
		return nil, FormatError(err)
	}

	return memoMessageList, nil
}

func vacuumMemo(ctx context.Context, tx *sql.Tx) error {
	stmt := `
	DELETE FROM 
		memo 
	WHERE 
		creator_id NOT IN (
			SELECT 
				id 
			FROM 
				user
		)`
	_, err := tx.ExecContext(ctx, stmt)
	if err != nil {
		return FormatError(err)
	}

	return nil
}

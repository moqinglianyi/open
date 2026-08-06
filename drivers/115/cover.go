package _115

import (
	"context"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/pkg/errors"
)

// ApiFileCover is the 115 web endpoint that (re)generates video covers.
// It accepts a comma separated list of file ids via the `file_id` form field.
const ApiFileCover = "https://webapi.115.com/files/cover"

// coverBatchSize caps how many file ids are submitted in a single request.
const coverBatchSize = 500

// GenerateCoverReq is the payload accepted by the "generate_cover" method.
type GenerateCoverReq struct {
	// Names restricts the operation to the listed children of the target folder.
	// Folders in the list are still traversed recursively.
	// When empty, every video below the target folder is processed.
	Names []string `json:"names"`
}

// GenerateCoverResp reports how many videos were submitted.
type GenerateCoverResp struct {
	Total int `json:"total"`
}

func (d *Pan115) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	switch args.Method {
	case "generate_cover":
		return d.generateCover(ctx, args)
	case "offline_list":
		return d.offlineList(ctx, args)
	case "offline_add":
		return d.offlineAdd(ctx, args)
	case "offline_delete":
		return d.offlineDelete(ctx, args)
	case "offline_clear":
		return d.offlineClear(ctx, args)
	default:
		return nil, errs.NotSupport
	}
}

func (d *Pan115) generateCover(ctx context.Context, args model.OtherArgs) (*GenerateCoverResp, error) {
	var req GenerateCoverReq
	if args.Data != nil {
		// args.Data arrives as a decoded JSON value, so round-trip it into the struct.
		raw, err := utils.Json.Marshal(args.Data)
		if err != nil {
			return nil, errors.WithMessage(err, "failed to encode generate_cover data")
		}
		if err = utils.Json.Unmarshal(raw, &req); err != nil {
			return nil, errors.WithMessage(err, "failed to decode generate_cover data")
		}
	}

	fileIDs := make([]string, 0)
	if args.Obj.IsDir() {
		if err := d.collectVideoIDs(ctx, args.Obj.GetID(), req.Names, &fileIDs); err != nil {
			return nil, err
		}
	} else if isVideo(args.Obj.GetName()) && args.Obj.GetID() != "" {
		fileIDs = append(fileIDs, args.Obj.GetID())
	}

	if len(fileIDs) == 0 {
		return nil, errors.New("no video found to generate cover")
	}
	if err := d.submitCovers(ctx, fileIDs); err != nil {
		return nil, err
	}
	return &GenerateCoverResp{Total: len(fileIDs)}, nil
}

// collectVideoIDs walks dirID and appends the id of every video it finds.
// names, when non-empty, filters the direct children of dirID only; matched
// folders are always traversed in full.
func (d *Pan115) collectVideoIDs(ctx context.Context, dirID string, names []string, fileIDs *[]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.WaitLimit(ctx); err != nil {
		return err
	}
	files, err := d.getFiles(dirID)
	if err != nil && !errors.Is(err, driver115.ErrNotExist) {
		return err
	}

	var filter map[string]struct{}
	if len(names) > 0 {
		filter = make(map[string]struct{}, len(names))
		for _, name := range names {
			filter[name] = struct{}{}
		}
	}

	for _, file := range files {
		if filter != nil {
			if _, ok := filter[file.GetName()]; !ok {
				continue
			}
		}
		if file.IsDir() {
			if err := d.collectVideoIDs(ctx, file.GetID(), nil, fileIDs); err != nil {
				return err
			}
			continue
		}
		if isVideo(file.GetName()) && file.GetID() != "" {
			*fileIDs = append(*fileIDs, file.GetID())
		}
	}
	return nil
}

func (d *Pan115) submitCovers(ctx context.Context, fileIDs []string) error {
	for start := 0; start < len(fileIDs); start += coverBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.WaitLimit(ctx); err != nil {
			return err
		}
		end := min(start+coverBatchSize, len(fileIDs))
		result := driver115.BasicResp{}
		req := d.client.NewRequest().
			SetContext(ctx).
			SetFormData(map[string]string{
				"file_id": strings.Join(fileIDs[start:end], ","),
				"show":    "1",
			}).
			SetResult(&result).
			ForceContentType("application/json;charset=UTF-8")
		resp, err := req.Post(ApiFileCover)
		if err = driver115.CheckErr(err, &result, resp); err != nil {
			return errors.WithMessage(err, "failed to generate cover")
		}
	}
	return nil
}

func isVideo(name string) bool {
	return utils.GetFileType(name) == conf.VIDEO
}

// ── offline download helpers ──────────────────────────────────────────────────

// OfflineListReq is the payload for "offline_list".
type OfflineListReq struct {
	Page int64 `json:"page"` // 1-based; 0 treated as 1
}

// OfflineAddReq is the payload for "offline_add".
type OfflineAddReq struct {
	URLs []string `json:"urls"`
}

// OfflineDeleteReq is the payload for "offline_delete".
type OfflineDeleteReq struct {
	Hashes      []string `json:"hashes"`
	DeleteFiles bool     `json:"delete_files"`
}

// OfflineClearReq is the payload for "offline_clear".
type OfflineClearReq struct {
	// 0 = completed tasks only, 1 = all tasks
	Flag int64 `json:"flag"`
}

func decodeData(data interface{}, dst interface{}) error {
	if data == nil {
		return nil
	}
	raw, err := utils.Json.Marshal(data)
	if err != nil {
		return err
	}
	return utils.Json.Unmarshal(raw, dst)
}

func (d *Pan115) offlineList(ctx context.Context, args model.OtherArgs) (*driver115.OfflineTaskResp, error) {
	var req OfflineListReq
	if err := decodeData(args.Data, &req); err != nil {
		return nil, errors.WithMessage(err, "bad offline_list data")
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	resp, err := d.client.ListOfflineTask(page - 1) // library uses 0-based
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (d *Pan115) offlineAdd(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var req OfflineAddReq
	if err := decodeData(args.Data, &req); err != nil {
		return nil, errors.WithMessage(err, "bad offline_add data")
	}
	if len(req.URLs) == 0 {
		return nil, errors.New("no URLs provided")
	}
	dirID := args.Obj.GetID()
	if !args.Obj.IsDir() {
		dirID = "0"
	}
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	hashes, err := d.client.AddOfflineTaskURIs(req.URLs, dirID, driver115.WithAppVer(appVer))
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"hashes": hashes, "count": len(hashes)}, nil
}

func (d *Pan115) offlineDelete(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var req OfflineDeleteReq
	if err := decodeData(args.Data, &req); err != nil {
		return nil, errors.WithMessage(err, "bad offline_delete data")
	}
	if len(req.Hashes) == 0 {
		return nil, errors.New("no hashes provided")
	}
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	if err := d.client.DeleteOfflineTasks(req.Hashes, req.DeleteFiles); err != nil {
		return nil, err
	}
	return map[string]interface{}{"deleted": len(req.Hashes)}, nil
}

func (d *Pan115) offlineClear(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var req OfflineClearReq
	if err := decodeData(args.Data, &req); err != nil {
		return nil, errors.WithMessage(err, "bad offline_clear data")
	}
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	if err := d.client.ClearOfflineTasks(req.Flag); err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true}, nil
}

var _ driver.Other = (*Pan115)(nil)

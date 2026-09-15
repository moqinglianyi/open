package pikpak

import (
	"context"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// CloudDownloadListResp is the response used by the OpenList PikPak modal.
type CloudDownloadListResp struct {
	Tasks []OfflineTask `json:"tasks"`
	Count int           `json:"count"`
}

type cloudDownloadAddReq struct {
	URLs []string `json:"urls"`
}
type cloudDownloadDeleteReq struct {
	IDs []string `json:"ids"`
}
type cloudDownloadClearReq struct {
	Flag int `json:"flag"`
}

func decodeCloudDownloadData(data interface{}, dst interface{}) error {
	if data == nil {
		return nil
	}
	raw, err := utils.Json.Marshal(data)
	if err != nil {
		return err
	}
	return utils.Json.Unmarshal(raw, dst)
}

// Other implements a small, PikPak-specific cloud download API. It deliberately
// uses different method names from the generic OpenList offline-download modal.
func (d *PikPak) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	switch args.Method {
	case "pikpak_cloud_download_list":
		tasks, err := d.OfflineList(ctx, "", nil)
		if err != nil {
			return nil, err
		}
		// Only show tasks created in the directory currently open in OpenList.
		dirID := args.Obj.GetID()
		if dirID != "" {
			filtered := tasks[:0]
			for _, task := range tasks {
				if task.ReferenceResource.ParentID == dirID {
					filtered = append(filtered, task)
				}
			}
			tasks = filtered
		}
		return &CloudDownloadListResp{Tasks: tasks, Count: len(tasks)}, nil
	case "pikpak_cloud_download_add":
		var req cloudDownloadAddReq
		if err := decodeCloudDownloadData(args.Data, &req); err != nil {
			return nil, fmt.Errorf("bad cloud download data: %w", err)
		}
		urls := make([]string, 0, len(req.URLs))
		for _, url := range req.URLs {
			if s := strings.TrimSpace(url); s != "" {
				urls = append(urls, s)
			}
		}
		if len(urls) == 0 {
			return nil, fmt.Errorf("no URLs provided")
		}
		dirID := args.Obj.GetID()
		if dirID == "" {
			dirID = "0"
		}
		ids := make([]string, 0, len(urls))
		for _, url := range urls {
			task, err := d.OfflineDownload(ctx, url, &model.ObjThumb{Object: model.Object{ID: dirID, IsFolder: true}}, "")
			if err != nil {
				return nil, err
			}
			ids = append(ids, task.ID)
		}
		return map[string]interface{}{"ids": ids, "count": len(ids)}, nil
	case "pikpak_cloud_download_delete":
		var req cloudDownloadDeleteReq
		if err := decodeCloudDownloadData(args.Data, &req); err != nil {
			return nil, fmt.Errorf("bad cloud download delete data: %w", err)
		}
		if len(req.IDs) == 0 {
			return nil, fmt.Errorf("no task ids provided")
		}
		if err := d.DeleteOfflineTasks(ctx, req.IDs, false); err != nil {
			return nil, err
		}
		return map[string]interface{}{"deleted": len(req.IDs)}, nil
	case "pikpak_cloud_download_clear":
		var req cloudDownloadClearReq
		if err := decodeCloudDownloadData(args.Data, &req); err != nil {
			return nil, fmt.Errorf("bad cloud download clear data: %w", err)
		}
		tasks, err := d.OfflineList(ctx, "", nil)
		if err != nil {
			return nil, err
		}
		dirID := args.Obj.GetID()
		ids := make([]string, 0)
		for _, task := range tasks {
			if dirID != "" && task.ReferenceResource.ParentID != dirID {
				continue
			}
			if req.Flag == 0 && task.Phase != "PHASE_TYPE_COMPLETE" {
				continue
			}
			if task.ID != "" {
				ids = append(ids, task.ID)
			}
		}
		if len(ids) > 0 {
			if err := d.DeleteOfflineTasks(ctx, ids, false); err != nil {
				return nil, err
			}
		}
		return map[string]interface{}{"ok": true, "deleted": len(ids)}, nil
	default:
		return nil, errs.NotSupport
	}
}

var _ interface {
	Other(context.Context, model.OtherArgs) (interface{}, error)
} = (*PikPak)(nil)

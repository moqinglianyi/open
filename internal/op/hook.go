package op

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/rawpreview"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// Obj
type ObjsUpdateHook = func(ctx context.Context, parent string, objs []model.Obj)

var (
	objsUpdateHooks = make([]ObjsUpdateHook, 0)
)

func RegisterObjsUpdateHook(hook ObjsUpdateHook) {
	objsUpdateHooks = append(objsUpdateHooks, hook)
}

func HandleObjsUpdateHook(ctx context.Context, parent string, objs []model.Obj) {
	for _, hook := range objsUpdateHooks {
		hook(ctx, parent, objs)
	}
}

// Setting
type SettingItemHook func(item *model.SettingItem) error

var settingItemHooks = map[string]SettingItemHook{
	conf.VideoTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.VideoTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.AudioTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.AudioTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ImageTypes: func(item *model.SettingItem) error {
		applyImageTypes(item.Value)
		return nil
	},
	conf.TextTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.TextTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ProxyTypes: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.ProxyTypes] = strings.Split(item.Value, ",")
		return nil
	},
	conf.ProxyIgnoreHeaders: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.ProxyIgnoreHeaders] = strings.Split(item.Value, ",")
		return nil
	},
	conf.PrivacyRegs: func(item *model.SettingItem) error {
		regStrs := strings.Split(item.Value, "\n")
		regs := make([]*regexp.Regexp, 0, len(regStrs))
		for _, regStr := range regStrs {
			reg, err := regexp.Compile(regStr)
			if err != nil {
				return errors.WithStack(err)
			}
			regs = append(regs, reg)
		}
		conf.PrivacyReg = regs
		return nil
	},
	conf.FilenameCharMapping: func(item *model.SettingItem) error {
		err := utils.Json.UnmarshalFromString(item.Value, &conf.FilenameCharMap)
		if err != nil {
			return err
		}
		log.Debugf("filename char mapping: %+v", conf.FilenameCharMap)
		return nil
	},
	conf.IgnoreDirectLinkParams: func(item *model.SettingItem) error {
		conf.SlicesMap[conf.IgnoreDirectLinkParams] = strings.Split(item.Value, ",")
		return nil
	},
	conf.RawPreviewEnabled: func(item *model.SettingItem) error {
		rawpreview.SetEnabled(item.Value == "true")
		reapplyImageTypes()
		return nil
	},
	conf.RawPreviewNegotiate: func(item *model.SettingItem) error {
		rawpreview.SetNegotiate(item.Value == "true")
		return nil
	},
	conf.RawPreviewTypes: func(item *model.SettingItem) error {
		rawpreview.SetExtensions(strings.Split(item.Value, ","))
		reapplyImageTypes()
		return nil
	},
	conf.RawPreviewThumbSize: func(item *model.SettingItem) error {
		px, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil {
			return errors.WithStack(err)
		}
		rawpreview.SetThumbSize(px)
		RawPreviewCacheClear()
		return nil
	},
	conf.RawPreviewMaxSize: func(item *model.SettingItem) error {
		mb, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil {
			return errors.WithStack(err)
		}
		rawpreview.SetMaxPreviewBytes(int64(mb) << 20)
		return nil
	},
	conf.RawPreviewCacheSize: func(item *model.SettingItem) error {
		mb, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil {
			return errors.WithStack(err)
		}
		rawpreview.SetCacheBytes(int64(mb) << 20)
		return nil
	},
}

func RegisterSettingItemHook(key string, hook SettingItemHook) {
	settingItemHooks[key] = hook
}

// imageTypesValue keeps the raw image_types setting so the published list can be
// recomputed when the RAW preview settings change.
var imageTypesValue atomic.Pointer[string]

// applyImageTypes publishes the image extension list. While RAW preview
// extraction is on, the camera RAW extensions are added to it, so RAW photos are
// typed as images without the operator having to edit image_types by hand.
func applyImageTypes(value string) {
	imageTypesValue.Store(&value)
	types := strings.Split(value, ",")
	if rawpreview.Enabled() {
		seen := make(map[string]struct{}, len(types))
		for _, t := range types {
			seen[strings.ToLower(strings.TrimSpace(t))] = struct{}{}
		}
		for _, e := range rawpreview.Extensions() {
			if _, ok := seen[e]; !ok {
				types = append(types, e)
			}
		}
	}
	conf.SlicesMap[conf.ImageTypes] = types
}

// reapplyImageTypes republishes the list from the stored image_types value. The
// RAW preview hooks call it because turning extraction on or off, or changing
// the RAW extension list, changes what counts as an image. It is a no-op until
// the image_types hook has run once.
func reapplyImageTypes() {
	if v := imageTypesValue.Load(); v != nil {
		applyImageTypes(*v)
	}
}

func HandleSettingItemHook(item *model.SettingItem) (hasHook bool, err error) {
	if hook, ok := settingItemHooks[item.Key]; ok {
		return true, hook(item)
	}
	return false, nil
}

// Storage
type StorageHook func(typ string, storage driver.Driver)

var storageHooks = make([]StorageHook, 0)

func callStorageHooks(typ string, storage driver.Driver) {
	for _, hook := range storageHooks {
		hook(typ, storage)
	}
}

func RegisterStorageHook(hook StorageHook) {
	storageHooks = append(storageHooks, hook)
}

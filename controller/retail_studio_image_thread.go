package controller

import (
	"bufio"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

var errImageParent = errors.New("image conversation has changed")

// studioImageRole accepts only result/reference and their canonical per-turn suffixes.
func studioImageRole(role string) bool {
	kind, index, indexed := strings.Cut(role, "-")
	if kind != "reference" && kind != "result" {
		return false
	}
	if !indexed {
		return true
	}
	i, err := strconv.Atoi(index)
	return err == nil && i >= 0 && strconv.Itoa(i) == index
}

// continueStudioImage appends the new turn to the authenticated account's saved conversation.
// The caller holds the owner lock and cleans up the child on any failure before retiring the parent.
func continueStudioImage(dir string, in *studioIntent) error {
	row := &in.Record
	parentKey := filepath.Join(dir, in.Parent)
	f, err := os.Open(parentKey + ".record")
	if errors.Is(err, os.ErrNotExist) {
		return errImageParent
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	head, err := readStudioHistoryHead(r)
	if err != nil {
		return err
	}
	if head.ID != in.Parent || head.Mode != "image" || head.Product != row.Product || head.expired(time.Now()) {
		return errImageParent
	}
	var prior studioHistoryRecord
	if err = common.DecodeJson(r, &prior); err != nil {
		return err
	}
	if len(prior.Entries) == 0 {
		return errImageParent
	}
	childKey := filepath.Join(dir, row.ID)
	for i := range prior.Entries {
		entry := &prior.Entries[i]
		// Older snapshots store media under unsuffixed names or inline. Give every inherited
		// file a child-owned link before deleting the parent, so arbitrary history URLs are never fetched.
		for _, attachment := range []struct {
			kind  string
			value *string
		}{{"reference", &entry.Reference}, {"result", &entry.Image}} {
			src := *attachment.value
			if src == "" {
				continue
			}
			role := attachment.kind + "-" + strconv.Itoa(i)
			dest := childKey + "." + role
			switch src {
			case studioMediaPrefix + in.Parent + "/" + role:
				err = os.Link(parentKey+"."+role, dest)
			case studioMediaPrefix + in.Parent + "/" + attachment.kind:
				err = os.Link(parentKey+"."+attachment.kind, dest)
			default:
				if !safeStudioRaster(src) {
					return errStudioMedia
				}
				_, encoded, _ := strings.Cut(src, ",")
				var data []byte
				data, err = base64.StdEncoding.DecodeString(encoded)
				if err == nil {
					err = storeStudioImage(dest, data)
				}
			}
			if err != nil {
				return err
			}
			*attachment.value = studioMediaPrefix + row.ID + "/" + role
		}
	}
	next := row.Entries[0]
	next.ID = prior.Entries[len(prior.Entries)-1].ID + 1
	// 历史文件仅用于回看。本轮是否改图由新消息显式提交的参考图决定。
	row.Entries = append(prior.Entries, next)
	return nil
}

// studioImagePrompt fits the most recent complete instructions and the entire latest prompt
// into the image API limit. Failed turns and older prompts that no longer fit are skipped.
func studioImagePrompt(entries []studioHistoryEntry) string {
	last := entries[len(entries)-1].Prompt
	const boundary = "\n\nCurrent request:\n"
	const header = "Previous requests:\n"
	budget := 32000 - utf8.RuneCountInString(last) - len(boundary) - len(header)
	start := len(entries) - 1
	for i := start - 1; i >= 0; i-- {
		e := entries[i]
		if e.Complete && e.Error == "" {
			budget -= utf8.RuneCountInString(e.Prompt) + 1
			if budget < 0 {
				break
			}
		}
		start = i
	}
	var prior strings.Builder
	for _, e := range entries[start : len(entries)-1] {
		if e.Complete && e.Error == "" {
			prior.WriteString(e.Prompt)
			prior.WriteByte('\n')
		}
	}
	if prior.Len() == 0 {
		return last
	}
	return header + prior.String() + boundary + last
}

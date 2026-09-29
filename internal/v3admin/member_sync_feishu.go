package v3admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const maxFeishuMembers = 10000
const maxFeishuDepartments = 1000
const maxFeishuPages = 2500
const feishuOrigin = "https://open.feishu.cn"

var feishuBindingID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
var feishuDepartmentID = regexp.MustCompile(`^(0|od-[A-Za-z0-9_-]{8,100})$`)
var feishuOpenID = regexp.MustCompile(`^ou_[A-Za-z0-9_-]{8,120}$`)

// FeishuDirectoryConfig binds exactly one tenant/application/root to a stable
// local identity namespace. Root 0 forces Feishu's full-directory permission
// check; AddressBookScopeApproved records the operator's verified app scope.
// API pagination alone cannot prove a stable tenant-wide snapshot, so no
// absent user is disabled.
type FeishuDirectoryConfig struct {
	AppID                    string
	AppSecret                string
	BindingID                string
	RootDepartmentID         string
	AddressBookScopeApproved bool
}

type FeishuDirectoryReader struct {
	BindingID        string
	RootDepartmentID string
	appID            string
	appSecret        string
	client           *http.Client
}

func NewFeishuDirectoryReader(config FeishuDirectoryConfig) (*FeishuDirectoryReader, error) {
	if config.AppID == "" || len(config.AppID) > 128 || config.AppSecret == "" || len(config.AppSecret) > 1024 ||
		!feishuBindingID.MatchString(config.BindingID) ||
		config.RootDepartmentID != "0" ||
		!config.AddressBookScopeApproved {
		return nil, errMemberSyncUnavailable
	}
	return &FeishuDirectoryReader{
		BindingID: config.BindingID, RootDepartmentID: config.RootDepartmentID,
		appID: config.AppID, appSecret: config.AppSecret,
		client: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}, nil
}

type feishuEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func (r *FeishuDirectoryReader) request(ctx context.Context, method, address, bearer string, body any, target any) error {
	if r == nil || r.client == nil || !strings.HasPrefix(address, feishuOrigin+"/open-apis/") {
		return errMemberSyncUnavailable
	}
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errMemberSyncUnavailable
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, input)
	if err != nil {
		return errMemberSyncUnavailable
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(req)
	if err != nil {
		return errMemberSyncUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 2<<20 {
		return errMemberSyncUnavailable
	}
	var envelope feishuEnvelope
	decoder := json.NewDecoder(io.LimitReader(response.Body, (2<<20)+1))
	if err = decoder.Decode(&envelope); err != nil || envelope.Code != 0 || len(envelope.Data) == 0 {
		return errMemberSyncUnavailable
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errMemberSyncUnavailable
	}
	if err = json.Unmarshal(envelope.Data, target); err != nil {
		return errMemberSyncUnavailable
	}
	return nil
}

func (r *FeishuDirectoryReader) accessToken(ctx context.Context) (string, error) {
	// Authentication returns token and expiry at the top level, unlike other
	// Feishu APIs. Never retain or log the response body.
	encoded, _ := json.Marshal(map[string]string{"app_id": r.appID, "app_secret": r.appSecret})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		feishuOrigin+"/open-apis/auth/v3/tenant_access_token/internal", bytes.NewReader(encoded))
	if err != nil {
		return "", errMemberSyncUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", errMemberSyncUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > 4096 {
		return "", errMemberSyncUnavailable
	}
	var body struct {
		Code              int    `json:"code"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4097))
	if decoder.Decode(&body) != nil || body.Code != 0 || body.TenantAccessToken == "" ||
		len(body.TenantAccessToken) > 2048 || body.Expire < 120 {
		return "", errMemberSyncUnavailable
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", errMemberSyncUnavailable
	}
	return body.TenantAccessToken, nil
}

type feishuPage[T any] struct {
	HasMore   *bool  `json:"has_more"`
	PageToken string `json:"page_token"`
	Items     []T    `json:"items"`
}

type feishuDepartment struct {
	OpenDepartmentID string `json:"open_department_id"`
	Name             string `json:"name"`
}

type feishuUser struct {
	OpenID string `json:"open_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Status *struct {
		IsFrozen    *bool `json:"is_frozen"`
		IsResigned  *bool `json:"is_resigned"`
		IsActivated *bool `json:"is_activated"`
		IsExited    *bool `json:"is_exited"`
		IsUnjoin    *bool `json:"is_unjoin"`
	} `json:"status"`
}

func (r *FeishuDirectoryReader) ReadSnapshot(ctx context.Context) (feishuSnapshot, error) {
	if r == nil || r.client == nil {
		return feishuSnapshot{}, errMemberSyncUnavailable
	}
	out := feishuSnapshot{BindingID: r.BindingID, RootDepartment: r.RootDepartmentID}
	token, err := r.accessToken(ctx)
	if err != nil {
		return out, errMemberSyncUnavailable
	}
	type visit struct{ ID, Name string }
	queue := []visit{{ID: r.RootDepartmentID}}
	visited, identities := map[string]bool{}, map[string]int{}
	pages := 0
	for len(queue) > 0 {
		if len(visited) >= maxFeishuDepartments {
			return out, errMemberSyncUnavailable
		}
		department := queue[0]
		queue = queue[1:]
		if visited[department.ID] {
			return out, errMemberSyncUnavailable
		}
		visited[department.ID] = true
		userQuery := url.Values{"department_id": {department.ID}, "department_id_type": {"open_department_id"},
			"user_id_type": {"open_id"}, "page_size": {"50"}}
		userURL := feishuOrigin + "/open-apis/contact/v3/users/find_by_department"
		err = eachFeishuPage(r, ctx, token, userURL, userQuery, &pages, func(page feishuPage[feishuUser]) error {
			for _, item := range page.Items {
				if !feishuOpenID.MatchString(item.OpenID) || item.Status == nil ||
					item.Status.IsFrozen == nil || item.Status.IsResigned == nil ||
					item.Status.IsActivated == nil || item.Status.IsExited == nil || item.Status.IsUnjoin == nil {
					return errMemberSyncUnavailable
				}
				if *item.Status.IsFrozen || *item.Status.IsResigned || !*item.Status.IsActivated ||
					*item.Status.IsExited || *item.Status.IsUnjoin {
					continue
				}
				externalID := r.BindingID + ":" + item.OpenID
				row := feishuMemberRow{ExternalID: externalID, DepartmentID: department.ID,
					Email: strings.ToLower(strings.TrimSpace(item.Email)),
					Name:  strings.TrimSpace(item.Name), Department: department.Name}
				if index, seen := identities[externalID]; seen {
					// Multiple department membership is valid. Select the
					// smallest department ID so page order cannot change the
					// display department between runs. Root 0 has no verified
					// name, so direct root members keep it empty.
					previous := out.Rows[index]
					if previous.Email != row.Email || previous.Name != row.Name {
						return errMemberSyncUnavailable
					}
					if row.DepartmentID < previous.DepartmentID {
						out.Rows[index] = row
					}
					continue
				}
				identities[externalID] = len(out.Rows)
				out.Rows = append(out.Rows, row)
				if len(out.Rows) > maxFeishuMembers {
					return errMemberSyncUnavailable
				}
			}
			return nil
		})
		if err != nil {
			return out, errMemberSyncUnavailable
		}
		childQuery := url.Values{"department_id_type": {"open_department_id"}, "user_id_type": {"open_id"},
			"fetch_child": {"false"}, "page_size": {"50"}}
		childURL := feishuOrigin + "/open-apis/contact/v3/departments/" + url.PathEscape(department.ID) + "/children"
		err = eachFeishuPage(r, ctx, token, childURL, childQuery, &pages, func(page feishuPage[feishuDepartment]) error {
			for _, child := range page.Items {
				if !feishuDepartmentID.MatchString(child.OpenDepartmentID) ||
					child.OpenDepartmentID == department.ID || strings.TrimSpace(child.Name) == "" ||
					len(child.Name) > 200 {
					return errMemberSyncUnavailable
				}
				queue = append(queue, visit{ID: child.OpenDepartmentID, Name: strings.TrimSpace(child.Name)})
				if len(queue)+len(visited) > maxFeishuDepartments {
					return errMemberSyncUnavailable
				}
			}
			return nil
		})
		if err != nil {
			return out, errMemberSyncUnavailable
		}
	}
	out.Complete = true
	return out, nil
}

func eachFeishuPage[T any](r *FeishuDirectoryReader, ctx context.Context, bearer, endpoint string,
	query url.Values, total *int, apply func(feishuPage[T]) error) error {
	seenTokens := map[string]bool{}
	for {
		(*total)++
		if *total > maxFeishuPages {
			return errMemberSyncUnavailable
		}
		var page feishuPage[T]
		if err := r.request(ctx, http.MethodGet, endpoint+"?"+query.Encode(), bearer, nil, &page); err != nil {
			return err
		}
		if page.HasMore == nil || page.Items == nil {
			return errMemberSyncUnavailable
		}
		if err := apply(page); err != nil {
			return err
		}
		if !*page.HasMore {
			return nil
		}
		if page.PageToken == "" || seenTokens[page.PageToken] {
			return errMemberSyncUnavailable
		}
		seenTokens[page.PageToken] = true
		query.Set("page_token", page.PageToken)
	}
}

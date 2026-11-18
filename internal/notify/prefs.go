package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/choibongseok/procure-ledger/internal/contract"
	"github.com/choibongseok/procure-ledger/internal/contract/audit"
)

// 수신 설정과 관리자 기본값(B36 단계 3, 명세 4.3·4.8).
// 본인은 알림 종류별 메일을 켜짐/꺼짐/기본값 따름으로 정하고(앱 안 알림은 끌 수 없다: D3), 업무 시간 외 묶음을 고른다.
// 본인 설정 변경은 개인 상태라 감사에 남기지 않는다. 관리자는 기본값·필수 여부·묶음 기본값·시간 설정을 정하고 감사에 남는다.
// 다른 사람의 수신 설정은 관리자도 보거나 바꾸지 못한다.

// PrefKind 는 내 수신 설정 한 줄이다.
type PrefKind struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	DefaultEnabled bool   `json:"defaultEnabled"`
	Locked         bool   `json:"locked"`    // 필수 알림: 꺼도 무시된다
	Mode           string `json:"mode"`      // on | off | default
	Effective      bool   `json:"effective"` // 지금 메일로 받는지
}

// MyPrefs 는 내 수신 설정 화면의 자료다.
type MyPrefs struct {
	MailAvailable bool       `json:"mailAvailable"` // 메일 서버가 설정되어 있는지(아니면 메일 칸은 안내만)
	Kinds         []PrefKind `json:"kinds"`
	Digest        struct {
		Mode      string `json:"mode"` // on | off | default
		DefaultOn bool   `json:"defaultOn"`
		Effective bool   `json:"effective"`
	} `json:"digest"`
	WorkHours    string `json:"workHours"`
	HoldMaxHours int    `json:"holdMaxHours"`
}

func validMode(m string) bool { return m == "on" || m == "off" || m == "default" }

// MyPrefs 는 로그인한 본인의 수신 설정이다(notification:read, 전 역할).
func (s *Service) MyPrefs(ctx context.Context) (MyPrefs, error) {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return MyPrefs{}, err
	}
	out := MyPrefs{MailAvailable: s.o.Mailer != nil}
	type def struct{ enabled, locked bool }
	defs := map[string]def{}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT kind, enabled, locked FROM notification_defaults WHERE channel = 'email'`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var k string
		var e, l int
		if err := rows.Scan(&k, &e, &l); err != nil {
			rows.Close()
			return out, err
		}
		defs[k] = def{e == 1, l == 1}
	}
	rows.Close()
	modes := map[string]string{}
	pr, err := s.o.DB.QueryContext(ctx, `SELECT kind, mode FROM notification_prefs WHERE user_id = ? AND channel = 'email'`, p.UserID)
	if err != nil {
		return out, err
	}
	for pr.Next() {
		var k, m string
		if err := pr.Scan(&k, &m); err != nil {
			pr.Close()
			return out, err
		}
		modes[k] = m
	}
	pr.Close()
	for _, k := range Kinds {
		d, ok := defs[k.Kind]
		if !ok {
			continue // 기본값 행이 없는 종류는 외부로 보내지 않으므로 설정 대상이 아니다
		}
		mode := modes[k.Kind]
		if mode == "" {
			mode = "default"
		}
		eff := d.enabled
		if mode == "on" {
			eff = true
		} else if mode == "off" && !(d.locked && d.enabled) {
			eff = false
		}
		out.Kinds = append(out.Kinds, PrefKind{Kind: k.Kind, Name: k.Name, DefaultEnabled: d.enabled, Locked: d.locked && d.enabled, Mode: mode, Effective: eff})
	}
	out.Digest.DefaultOn = strSetting(ctx, s.o.DB, "notify.digest_default", "0") == "1"
	out.Digest.Mode = "default"
	var dm string
	if err := s.o.DB.QueryRowContext(ctx, `SELECT digest FROM notification_user_opts WHERE user_id = ?`, p.UserID).Scan(&dm); err == nil {
		out.Digest.Mode = dm
	}
	out.Digest.Effective = out.Digest.DefaultOn
	if out.Digest.Mode != "default" {
		out.Digest.Effective = out.Digest.Mode == "on"
	}
	st, en := workWindow(ctx, s.o.DB)
	out.WorkHours = fmt.Sprintf("%02d:%02d~%02d:%02d", st/60, st%60, en/60, en%60)
	out.HoldMaxHours = intSetting(ctx, s.o.DB, "notify.hold_max_hours", 72)
	return out, nil
}

// SetMyPref 는 알림 종류 하나의 메일 수신을 정한다(on·off·default). 필수 알림은 끌 수 없다. 감사에 남기지 않는다(개인 상태).
func (s *Service) SetMyPref(ctx context.Context, kind, mode string) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return err
	}
	if _, ok := KindByCode(kind); !ok {
		return valErr("알 수 없는 알림 종류입니다.")
	}
	if !validMode(mode) {
		return valErr("수신 설정은 켜짐·꺼짐·기본값 따름 중에서 고르세요.")
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var enabled, locked int
		if err := tx.QueryRowContext(ctx, `SELECT enabled, locked FROM notification_defaults WHERE kind = ? AND channel = 'email'`, kind).Scan(&enabled, &locked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return valErr("이 알림은 메일로 보내지 않아 설정할 수 없습니다.")
			}
			return err
		}
		if mode == "off" && locked == 1 && enabled == 1 {
			return stateErr("필수 알림이라 끌 수 없습니다.")
		}
		if mode == "default" {
			_, err := tx.ExecContext(ctx, `DELETE FROM notification_prefs WHERE user_id = ? AND kind = ? AND channel = 'email'`, p.UserID, kind)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notification_prefs(user_id, kind, channel, mode, updated_at) VALUES (?, ?, 'email', ?, ?)
			ON CONFLICT(user_id, kind, channel) DO UPDATE SET mode = excluded.mode, updated_at = excluded.updated_at`, p.UserID, kind, mode, ts(s.now()))
		return err
	})
}

// SetMyDigest 는 업무 시간 외 묶음 발송 선택(on·off·default)이다.
func (s *Service) SetMyDigest(ctx context.Context, mode string) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationRead)
	if err != nil {
		return err
	}
	if !validMode(mode) {
		return valErr("묶음 발송은 켜짐·꺼짐·기본값 따름 중에서 고르세요.")
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		if mode == "default" {
			_, err := tx.ExecContext(ctx, `DELETE FROM notification_user_opts WHERE user_id = ?`, p.UserID)
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notification_user_opts(user_id, digest, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET digest = excluded.digest, updated_at = excluded.updated_at`, p.UserID, mode, ts(s.now()))
		return err
	})
}

// ---- 관리자 기본값과 설정 ----

// DefaultKind 는 관리자 기본값 한 줄이다.
type DefaultKind struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Locked  bool   `json:"locked"`
}

// SettingView 는 관리자가 바꿀 수 있는 설정 한 줄이다.
type SettingView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Unit  string `json:"unit"`
	Type  string `json:"type"` // int | clock | bool
	Min   int    `json:"min,omitempty"`
	Max   int    `json:"max,omitempty"`
	Hint  string `json:"hint,omitempty"`
}

type settingDef struct {
	Key, Label, Unit, Type, Default, Hint string
	Min, Max                              int
}

// settingDefs 는 관리자가 바꿀 수 있는 알림 설정이다(명세의 "초기값, 설정 가능" 수치). 범위를 벗어나면 저장하지 않는다.
var settingDefs = []settingDef{
	{"notify.work_start", "업무 시작 시각", "", "clock", "09:00", "업무 시간 외 묶음 발송의 기준(KST)", 0, 0},
	{"notify.work_end", "업무 종료 시각", "", "clock", "18:00", "시작 시각보다 늦어야 합니다", 0, 0},
	{"notify.hold_max_hours", "묶음 보류 상한", "시간", "int", "72", "이 시간이 지나면 업무 시간이 아니어도 보냅니다", 1, 720},
	{"notify.digest_default", "업무 시간 외 묶음 기본값", "", "bool", "0", "켜면 본인이 따로 정하지 않은 사람은 묶음으로 받습니다", 0, 1},
	{"notify.window_sec", "합치기 창", "초", "int", "60", "같은 사람·종류의 알림을 한 통으로 합치는 시간(0 이면 합치지 않음)", 0, 600},
	{"notify.max_attempts", "재시도 횟수", "회", "int", "5", "넘으면 포기합니다", 1, 10},
	{"notify.mail_per_min", "메일 분당 상한", "통", "int", "60", "서버 전체", 1, 600},
	{"notify.mail_per_user_hour", "한 사람 시간당 메일 상한", "통", "int", "30", "", 1, 600},
	{"notify.target_per_min", "웹훅 대상별 분당 상한", "건", "int", "20", "", 1, 600},
	{"notify.target_fail_pause", "웹훅 연속 실패 자동 중지", "회", "int", "5", "최종 실패가 이만큼 이어지면 대상을 멈춥니다", 1, 20},
	{"notify.keep_days", "발송 기록 보존", "일", "int", "365", "지난 기록은 정리됩니다", 30, 3650},
	{"notify.payment_due_bd", "지급 예정 알림 범위", "영업일", "int", "3", "오늘부터 이 영업일 안에 만기가 오는 미지급 채무를 알립니다", 1, 10},
	{"notify.close_check_start_bd", "월 마감 점검 알림 시작", "영업일째", "int", "3", "이번 달의 몇째 영업일부터 지난 달 마감 점검을 알릴지", 1, 10},
	{"notify.close_check_every_days", "월 마감 점검 알림 주기", "일", "int", "7", "같은 사람에게 이 간격으로 한 번씩", 1, 30},
}

func findDef(key string) (settingDef, bool) {
	for _, d := range settingDefs {
		if d.Key == key {
			return d, true
		}
	}
	return settingDef{}, false
}

// normalizeSetting 은 값을 검증하고 저장 형식으로 돌려준다.
func normalizeSetting(d settingDef, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch d.Type {
	case "clock":
		m, ok := ParseClock(raw)
		if !ok {
			return "", valErr("%s: 시각은 09:00 같은 형식으로 입력하세요.", d.Label)
		}
		return fmt.Sprintf("%02d:%02d", m/60, m%60), nil
	case "bool":
		if raw != "0" && raw != "1" {
			return "", valErr("%s: 켜짐 또는 꺼짐 중에서 고르세요.", d.Label)
		}
		return raw, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < d.Min || n > d.Max {
		return "", valErr("%s: %d 이상 %d 이하의 정수를 입력하세요.", d.Label, d.Min, d.Max)
	}
	return strconv.Itoa(n), nil
}

// Defaults 는 관리자 기본값과 설정이다(notification:manage).
type Defaults struct {
	Kinds          []DefaultKind `json:"kinds"`
	Settings       []SettingView `json:"settings"`
	MailConfigured bool          `json:"mailConfigured"`
}

// Defaults 는 관리자 기본값 화면의 자료다.
func (s *Service) Defaults(ctx context.Context) (Defaults, error) {
	if _, err := contract.Authorize(ctx, contract.PermNotificationManage); err != nil {
		return Defaults{}, err
	}
	out := Defaults{MailConfigured: s.o.Mailer != nil}
	rows, err := s.o.DB.QueryContext(ctx, `SELECT kind, enabled, locked FROM notification_defaults WHERE channel = 'email'`)
	if err != nil {
		return out, err
	}
	have := map[string]DefaultKind{}
	for rows.Next() {
		var k string
		var e, l int
		if err := rows.Scan(&k, &e, &l); err != nil {
			rows.Close()
			return out, err
		}
		have[k] = DefaultKind{Kind: k, Enabled: e == 1, Locked: l == 1}
	}
	rows.Close()
	for _, k := range Kinds {
		d := have[k.Kind] // 행이 없으면 꺼짐
		d.Kind, d.Name = k.Kind, k.Name
		out.Kinds = append(out.Kinds, d)
	}
	for _, d := range settingDefs {
		out.Settings = append(out.Settings, SettingView{Key: d.Key, Label: d.Label, Value: strSetting(ctx, s.o.DB, d.Key, d.Default), Unit: d.Unit, Type: d.Type, Min: d.Min, Max: d.Max, Hint: d.Hint})
	}
	return out, nil
}

// SetDefault 는 알림 종류의 메일 기본값과 필수 여부를 정한다(notification:manage). 이미 큐에 있는 건에는 영향이 없고 이후 이벤트부터 적용된다.
func (s *Service) SetDefault(ctx context.Context, kind string, enabled, locked bool) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return err
	}
	if _, ok := KindByCode(kind); !ok {
		return valErr("알 수 없는 알림 종류입니다.")
	}
	if locked && !enabled {
		return valErr("필수 알림은 켜진 상태여야 합니다.")
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var oe, ol int
		err := tx.QueryRowContext(ctx, `SELECT enabled, locked FROM notification_defaults WHERE kind = ? AND channel = 'email'`, kind).Scan(&oe, &ol)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_defaults(kind, channel, enabled, locked, updated_by, updated_at) VALUES (?, 'email', ?, ?, ?, ?)
			ON CONFLICT(kind, channel) DO UPDATE SET enabled = excluded.enabled, locked = excluded.locked, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			kind, b2i(enabled), b2i(locked), p.UserID, ts(s.now())); err != nil {
			return err
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.default_set", TargetType: audit.TargetNotification, TargetID: "default-" + kind, Result: audit.ResultSuccess,
			Diff: []audit.DiffItem{{Key: "notification_kind", New: KindName(kind)}, {Key: "channel", New: ChannelName("email")},
				{Key: "mail_default", Old: onOff(oe == 1), New: onOff(enabled)}, {Key: "required", Old: boolWord(ol == 1), New: boolWord(locked)}}})
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func onOff(b bool) string {
	if b {
		return "켜짐"
	}
	return "꺼짐"
}

// SetSettings 는 알림 설정 값을 바꾼다(notification:manage). 모든 값을 먼저 검증하고(하나라도 어기면 아무것도 저장하지 않는다), 바뀐 항목만 감사에 남긴다.
func (s *Service) SetSettings(ctx context.Context, values map[string]string) error {
	p, err := contract.Authorize(ctx, contract.PermNotificationManage)
	if err != nil {
		return err
	}
	norm := map[string]string{}
	for k, v := range values {
		d, ok := findDef(k)
		if !ok {
			return valErr("알 수 없는 설정입니다.")
		}
		nv, err := normalizeSetting(d, v)
		if err != nil {
			return err
		}
		norm[k] = nv
	}
	// 업무 시간은 시작이 끝보다 빨라야 한다(둘 중 하나만 바꿔도 현재 값과 함께 본다).
	st, en := strSetting(ctx, s.o.DB, "notify.work_start", "09:00"), strSetting(ctx, s.o.DB, "notify.work_end", "18:00")
	if v, ok := norm["notify.work_start"]; ok {
		st = v
	}
	if v, ok := norm["notify.work_end"]; ok {
		en = v
	}
	sm, _ := ParseClock(st)
	em, _ := ParseClock(en)
	if em <= sm {
		return valErr("업무 종료 시각은 시작 시각보다 늦어야 합니다.")
	}
	return s.o.Txs.WithTx(ctx, func(tx contract.Tx) error {
		var diff []audit.DiffItem
		for _, d := range settingDefs { // 정해진 순서로(감사 기록이 같은 입력에 같게)
			nv, ok := norm[d.Key]
			if !ok {
				continue
			}
			old := strSetting(ctx, tx, d.Key, d.Default)
			if old == nv {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO app_settings(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, d.Key, nv); err != nil {
				return err
			}
			diff = append(diff, audit.DiffItem{Key: "setting_name", New: d.Label}, audit.DiffItem{Key: "setting_value", Old: old, New: nv})
		}
		if len(diff) == 0 {
			return nil
		}
		return s.o.Audit.Record(ctx, tx, audit.Entry{Actor: p, Action: "notification.settings_set", TargetType: audit.TargetNotification, TargetID: "settings", Result: audit.ResultSuccess, Diff: diff})
	})
}

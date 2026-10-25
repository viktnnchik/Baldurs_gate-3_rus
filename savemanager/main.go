// BG3 Save Manager — менеджер сохранений Baldur's Gate 3 с веб-интерфейсом.
//
// Программа поднимает локальный сервер (только 127.0.0.1), открывает окно
// интерфейса и сама завершается, когда окно закрыто.
package main

import (
	"archive/zip"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

type config struct {
	SavesDir    string `json:"savesDir"`
	AutoMinutes int    `json:"autoMinutes"` // 0 — автокопии выключены
	KeepAuto    int    `json:"keepAuto"`    // сколько автокопий хранить
}

var (
	mu        sync.Mutex
	cfg       config
	backupDir string
	token     string
	lastPing  = time.Now()
	pinged    bool
)

func defaultSavesDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "Larian Studios", "Baldur's Gate 3", "PlayerProfiles", "Public", "Savegames", "Story")
}

func cfgPath() string { return filepath.Join(backupDir, "config.json") }

func loadConfig() {
	cfg = config{SavesDir: defaultSavesDir(), KeepAuto: 20}
	if data, err := os.ReadFile(cfgPath()); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.SavesDir == "" {
		cfg.SavesDir = defaultSavesDir()
	}
	if cfg.KeepAuto <= 0 {
		cfg.KeepAuto = 20
	}
}

func saveConfig() {
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath(), data, 0o644)
}

func main() {
	home, _ := os.UserHomeDir()
	backupDir = filepath.Join(home, "Documents", "BG3_SaveBackups")
	if v := os.Getenv("BG3_BACKUP_DIR"); v != "" {
		backupDir = v
	}
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		fatal(err)
	}
	loadConfig()

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	addr := ln.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("t") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/api/state", api(handleState))
	mux.HandleFunc("/api/ping", api(func(w http.ResponseWriter, r *http.Request) error {
		mu.Lock()
		lastPing, pinged = time.Now(), true
		mu.Unlock()
		return reply(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("/api/backup", api(handleBackup))
	mux.HandleFunc("/api/restore", api(handleRestore))
	mux.HandleFunc("/api/delete", api(handleDelete))
	mux.HandleFunc("/api/settings", api(handleSettings))
	mux.HandleFunc("/api/openfolder", api(handleOpenFolder))

	go autoBackupLoop()
	go func() {
		for {
			time.Sleep(2 * time.Second)
			mu.Lock()
			gone := pinged && time.Since(lastPing) > 15*time.Second
			noShow := !pinged && time.Since(lastPing) > 60*time.Second
			mu.Unlock()
			if gone || noShow {
				os.Exit(0)
			}
		}
	}()

	url := "http://" + addr + "/?t=" + token
	if os.Getenv("BG3_NO_BROWSER") == "" {
		openWindow(url)
	} else {
		fmt.Println(url)
	}
	if err := http.Serve(ln, mux); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ошибка:", err)
	os.Exit(1)
}

// openWindow открывает интерфейс как окно приложения Edge/Chrome, а при их отсутствии — в браузере по умолчанию.
func openWindow(url string) {
	if runtime.GOOS == "windows" {
		for _, p := range []string{
			os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("ProgramFiles") + `\Google\Chrome\Application\chrome.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Google\Chrome\Application\chrome.exe`,
		} {
			if _, err := os.Stat(p); err == nil {
				if exec.Command(p, "--app="+url, "--window-size=980,720").Start() == nil {
					return
				}
			}
		}
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}

func api(h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := h(w, r); err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		}
	}
}

func reply(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(v)
}

type saveInfo struct {
	Name     string `json:"name"`
	Modified string `json:"modified"`
	Size     int64  `json:"size"`
}

type backupInfo struct {
	File    string   `json:"file"`
	Created string   `json:"created"`
	Size    int64    `json:"size"`
	Saves   []string `json:"saves"`
	Auto    bool     `json:"auto"`
}

func dirStats(dir string) (size int64, mod time.Time) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			size += info.Size()
			if info.ModTime().After(mod) {
				mod = info.ModTime()
			}
		}
		return nil
	})
	return
}

func listSaves() ([]saveInfo, error) {
	entries, err := os.ReadDir(cfg.SavesDir)
	if err != nil {
		return nil, err
	}
	var out []saveInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		size, mod := dirStats(filepath.Join(cfg.SavesDir, e.Name()))
		out = append(out, saveInfo{e.Name(), mod.Format(time.RFC3339), size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func listBackups() []backupInfo {
	entries, _ := os.ReadDir(backupDir)
	out := []backupInfo{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		b := backupInfo{File: e.Name(), Created: info.ModTime().Format(time.RFC3339), Size: info.Size(),
			Auto: strings.Contains(e.Name(), "_auto"), Saves: []string{}}
		if zr, err := zip.OpenReader(filepath.Join(backupDir, e.Name())); err == nil {
			seen := map[string]bool{}
			for _, f := range zr.File {
				if parts := strings.Split(f.Name, "/"); len(parts) >= 2 && !seen[parts[0]] {
					seen[parts[0]] = true
					b.Saves = append(b.Saves, parts[0])
				}
			}
			zr.Close()
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created > out[j].Created })
	return out
}

func handleState(w http.ResponseWriter, r *http.Request) error {
	mu.Lock()
	defer mu.Unlock()
	saves, err := listSaves()
	state := map[string]any{
		"savesDir": cfg.SavesDir, "backupDir": backupDir, "autoMinutes": cfg.AutoMinutes,
		"keepAuto": cfg.KeepAuto, "saves": saves, "backups": listBackups(),
	}
	if err != nil {
		state["savesError"] = "Папка сохранений не найдена: " + cfg.SavesDir
		state["saves"] = []saveInfo{}
	}
	return reply(w, state)
}

// makeBackup упаковывает выбранные сохранения (или все) в zip. Вызывать под mu.
func makeBackup(names []string, tag string) (string, error) {
	if len(names) == 0 {
		saves, err := listSaves()
		if err != nil {
			return "", err
		}
		for _, s := range saves {
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return "", errors.New("нет сохранений для копирования")
	}
	file := time.Now().Format("2006-01-02_15-04-05") + "_" + tag + ".zip"
	tmp := filepath.Join(backupDir, file+".part")
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(out)
	fail := func(err error) (string, error) {
		zw.Close()
		out.Close()
		os.Remove(tmp)
		return "", err
	}
	for _, n := range names {
		if n != filepath.Base(n) || n == "." || n == ".." {
			return fail(fmt.Errorf("недопустимое имя сохранения: %s", n))
		}
		root := filepath.Join(cfg.SavesDir, n)
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(cfg.SavesDir, p)
			info, err := d.Info()
			if err != nil {
				return err
			}
			hdr, _ := zip.FileInfoHeader(info)
			hdr.Name = filepath.ToSlash(rel)
			hdr.Method = zip.Store // .lsv уже сжаты
			wr, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(wr, f)
			return err
		})
		if err != nil {
			return fail(err)
		}
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return file, os.Rename(tmp, filepath.Join(backupDir, file))
}

func handleBackup(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Names []string `json:"names"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	mu.Lock()
	defer mu.Unlock()
	file, err := makeBackup(req.Names, "manual")
	if err != nil {
		return err
	}
	return reply(w, map[string]string{"file": file})
}

func safeBackupName(name string) (string, error) {
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".zip") {
		return "", errors.New("недопустимое имя копии")
	}
	return filepath.Join(backupDir, name), nil
}

func handleRestore(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return err
	}
	path, err := safeBackupName(req.File)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()

	// Сначала страхуемся: копия тех сохранений, которые будут перезаписаны.
	var existing []string
	seen := map[string]bool{}
	for _, f := range zr.File {
		if parts := strings.Split(f.Name, "/"); len(parts) >= 2 && !seen[parts[0]] {
			seen[parts[0]] = true
			if _, err := os.Stat(filepath.Join(cfg.SavesDir, parts[0])); err == nil {
				existing = append(existing, parts[0])
			}
		}
	}
	safety := ""
	if len(existing) > 0 {
		if safety, err = makeBackup(existing, "before-restore"); err != nil {
			return fmt.Errorf("не удалось сделать страховочную копию: %w", err)
		}
	}
	root := filepath.Clean(cfg.SavesDir)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		dst := filepath.Join(root, filepath.FromSlash(f.Name))
		if !strings.HasPrefix(dst, root+string(os.PathSeparator)) {
			return fmt.Errorf("небезопасный путь в копии: %s", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		_ = os.Chtimes(dst, f.Modified, f.Modified)
	}
	return reply(w, map[string]string{"safety": safety})
}

func handleDelete(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return err
	}
	path, err := safeBackupName(req.File)
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(path); err != nil {
		return err
	}
	return reply(w, map[string]bool{"ok": true})
}

func handleSettings(w http.ResponseWriter, r *http.Request) error {
	var req config
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if req.SavesDir != "" {
		cfg.SavesDir = strings.Trim(strings.TrimSpace(req.SavesDir), `"`)
	}
	if req.AutoMinutes < 0 || req.AutoMinutes > 24*60 {
		return errors.New("интервал автокопий: от 0 до 1440 минут")
	}
	cfg.AutoMinutes = req.AutoMinutes
	if req.KeepAuto > 0 {
		cfg.KeepAuto = req.KeepAuto
	}
	saveConfig()
	return reply(w, map[string]bool{"ok": true})
}

func handleOpenFolder(w http.ResponseWriter, r *http.Request) error {
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", backupDir).Start()
	}
	return reply(w, map[string]bool{"ok": true})
}

// autoBackupLoop делает автокопию, если с прошлой копии сохранения изменились.
func autoBackupLoop() {
	var last time.Time
	for {
		time.Sleep(20 * time.Second)
		mu.Lock()
		every := time.Duration(cfg.AutoMinutes) * time.Minute
		if every > 0 && time.Since(last) >= every {
			last = time.Now()
			saves, err := listSaves()
			if err == nil && len(saves) > 0 {
				newest, _ := time.Parse(time.RFC3339, saves[0].Modified)
				var lastBackup time.Time
				for _, b := range listBackups() {
					t, _ := time.Parse(time.RFC3339, b.Created)
					if t.After(lastBackup) {
						lastBackup = t
					}
				}
				if newest.After(lastBackup) {
					if _, err := makeBackup(nil, "auto"); err == nil {
						pruneAuto()
					}
				}
			}
		}
		mu.Unlock()
	}
}

func pruneAuto() {
	var autos []backupInfo
	for _, b := range listBackups() {
		if b.Auto {
			autos = append(autos, b)
		}
	}
	for i := cfg.KeepAuto; i < len(autos); i++ { // listBackups отсортирован от новых к старым
		_ = os.Remove(filepath.Join(backupDir, autos[i].File))
	}
}

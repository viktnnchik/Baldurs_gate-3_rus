// Установка русской озвучки (мода с .pak-файлами) для Baldur's Gate 3.
//
// Озвучка не вшита в exe: её можно указать ссылкой, путём к файлу (.zip/.pak)
// либо положить архив рядом с программой. Файлы из архива копируются в папку
// модов игры, а мод прописывается в modsettings.lsx (если в архиве есть info.json).
package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const voiceManifestName = "bg3_rus_voice_manifest.json"

// voiceManifest запоминает, что поставила программа, чтобы озвучку можно было убрать.
type voiceManifest struct {
	Files []string `json:"files"`
	UUIDs []string `json:"uuids"`
}

type modInfo struct {
	Mods []struct {
		Name      string      `json:"modName"`
		Folder    string      `json:"folderName"`
		UUID      string      `json:"uuid"`
		MD5       string      `json:"md5"`
		Version   json.Number `json:"version"`
		Version64 json.Number `json:"version64"`
	} `json:"Mods"`
}

// modsDir — папка модов пользователя (AppData\Local\Larian Studios\Baldur's Gate 3\Mods).
func modsDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "Larian Studios", "Baldur's Gate 3")
}

// findVoiceSource определяет, откуда брать озвучку: аргумент, файл рядом с exe или вопрос пользователю.
func findVoiceSource(src string) (string, error) {
	if src != "" {
		return src, nil
	}
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			l := strings.ToLower(e.Name())
			if e.IsDir() || !(strings.HasSuffix(l, ".zip") || strings.HasSuffix(l, ".pak")) {
				continue
			}
			if strings.Contains(l, "voice") || strings.Contains(l, "озвуч") || strings.Contains(l, "synth") {
				p := filepath.Join(d, e.Name())
				fmt.Println("Найден файл озвучки рядом с программой:", p)
				return p, nil
			}
		}
	}
	fmt.Println("Файл озвучки не найден. Скачайте русскую озвучку (например SynthVoiceRu)")
	fmt.Println("и укажите ссылку (http...) либо путь к скачанному .zip/.pak файлу.")
	p := strings.Trim(ask("Ссылка или путь (пусто — пропустить озвучку): "), `"`)
	if p == "" {
		return "", errors.New("источник озвучки не указан")
	}
	return p, nil
}

// fetchVoice скачивает озвучку по ссылке во временный файл или возвращает локальный путь.
func fetchVoice(src string) (path string, cleanup func(), err error) {
	noop := func() {}
	if !strings.HasPrefix(strings.ToLower(src), "http://") && !strings.HasPrefix(strings.ToLower(src), "https://") {
		if _, err := os.Stat(src); err != nil {
			return "", noop, fmt.Errorf("файл озвучки не найден: %s", src)
		}
		return src, noop, nil
	}
	fmt.Println("Скачиваю озвучку:", src)
	resp, err := http.Get(src)
	if err != nil {
		return "", noop, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", noop, fmt.Errorf("сервер вернул %s", resp.Status)
	}
	ext := ".zip"
	if strings.HasSuffix(strings.ToLower(strings.Split(src, "?")[0]), ".pak") {
		ext = ".pak"
	}
	f, err := os.CreateTemp("", "bg3voice_*"+ext)
	if err != nil {
		return "", noop, err
	}
	rm := func() { os.Remove(f.Name()) }
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		rm()
		return "", noop, err
	}
	f.Close()
	return f.Name(), rm, nil
}

func installVoice(locDir, src string) error {
	gameDir := filepath.Dir(filepath.Dir(locDir)) // ...\Baldurs Gate 3
	src, err := findVoiceSource(src)
	if err != nil {
		return err
	}
	path, cleanup, err := fetchVoice(src)
	if err != nil {
		return err
	}
	defer cleanup()

	mods := filepath.Join(modsDir(), "Mods")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		return err
	}
	man := voiceManifest{}
	var info *modInfo

	if strings.HasSuffix(strings.ToLower(path), ".pak") {
		dst := filepath.Join(mods, filepath.Base(src))
		if err := copyFile(path, dst); err != nil {
			return err
		}
		man.Files = append(man.Files, dst)
	} else {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return fmt.Errorf("не удалось открыть архив озвучки: %w", err)
		}
		defer zr.Close()
		for _, zf := range zr.File {
			if zf.FileInfo().IsDir() {
				continue
			}
			name := filepath.ToSlash(zf.Name)
			lower := strings.ToLower(name)
			base := filepath.Base(name)
			var dst string
			switch {
			case strings.HasSuffix(lower, "info.json"):
				if info == nil {
					info = readInfo(zf)
				}
				continue
			case strings.HasSuffix(lower, ".pak"):
				dst = filepath.Join(mods, base)
			case hasTopDir(name, "public", "mods", "generated", "localization"):
				// «сырой» мод: распаковывается в Data игры
				dst = filepath.Join(gameDir, "Data", filepath.FromSlash(trimToTopDir(name)))
			default:
				continue
			}
			if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(gameDir)) &&
				!strings.HasPrefix(filepath.Clean(dst), filepath.Clean(modsDir())) {
				return fmt.Errorf("небезопасный путь в архиве: %s", name)
			}
			if err := extractZipFile(zf, dst); err != nil {
				return err
			}
			man.Files = append(man.Files, dst)
		}
	}
	if len(man.Files) == 0 {
		return errors.New("в архиве нет .pak-файлов озвучки")
	}
	for _, f := range man.Files {
		fmt.Println("  установлено:", f)
	}

	if info != nil {
		uuids, err := registerMods(info)
		if err != nil {
			fmt.Println("Не удалось прописать мод в modsettings.lsx:", err)
			fmt.Println("Включите озвучку вручную в BG3 Mod Manager.")
		} else {
			man.UUIDs = uuids
			fmt.Println("Озвучка включена в modsettings.lsx")
		}
	} else if hasPak(man.Files) {
		fmt.Println("В архиве нет info.json: включите озвучку в BG3 Mod Manager (Import → Activate → Export to Game).")
	}

	data, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(gameDir, "Data", voiceManifestName), data, 0o644); err != nil {
		return err
	}
	fmt.Println("\nОзвучка установлена. В настройках игры отключите «Озвучивание реплик при нажатии», если оно мешает.")
	return nil
}

func removeVoice(locDir string) error {
	gameDir := filepath.Dir(filepath.Dir(locDir))
	mpath := filepath.Join(gameDir, "Data", voiceManifestName)
	data, err := os.ReadFile(mpath)
	if err != nil {
		return errors.New("установленная программой озвучка не найдена")
	}
	var man voiceManifest
	if err := json.Unmarshal(data, &man); err != nil {
		return err
	}
	for _, f := range man.Files {
		if err := os.Remove(f); err == nil {
			fmt.Println("  удалено:", f)
		}
	}
	if len(man.UUIDs) > 0 {
		if err := unregisterMods(man.UUIDs); err != nil {
			fmt.Println("modsettings.lsx не обновлён:", err)
		}
	}
	_ = os.Remove(mpath)
	fmt.Println("\nОзвучка удалена.")
	return nil
}

func hasPak(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f), ".pak") {
			return true
		}
	}
	return false
}

func hasTopDir(name string, tops ...string) bool {
	return trimToTopDir(name) != "" && func() bool {
		first := strings.ToLower(strings.Split(trimToTopDir(name), "/")[0])
		for _, t := range tops {
			if first == t {
				return true
			}
		}
		return false
	}()
}

// trimToTopDir отрезает лишнюю корневую папку архива перед Public/Mods/Generated/Localization.
func trimToTopDir(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "public", "mods", "generated", "localization":
			return strings.Join(parts[i:], "/")
		}
	}
	return ""
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func extractZipFile(zf *zip.File, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func readInfo(zf *zip.File) *modInfo {
	rc, err := zf.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	var info modInfo
	if json.NewDecoder(rc).Decode(&info) != nil || len(info.Mods) == 0 {
		return nil
	}
	return &info
}

const defaultModSettings = `<?xml version="1.0" encoding="UTF-8"?>
<save>
	<version major="4" minor="0" revision="9" build="328" />
	<region id="ModuleSettings">
		<node id="root">
			<children>
				<node id="ModOrder">
					<children>
						<node id="Module">
							<attribute id="UUID" type="FixedString" value="28ac9ce2-2aba-8cda-b3b5-6e922f71b6b8" />
						</node>
					</children>
				</node>
				<node id="Mods">
					<children>
						<node id="ModuleShortDesc">
							<attribute id="Folder" type="LSString" value="GustavDev" />
							<attribute id="MD5" type="LSString" value="" />
							<attribute id="Name" type="LSString" value="GustavDev" />
							<attribute id="UUID" type="FixedString" value="28ac9ce2-2aba-8cda-b3b5-6e922f71b6b8" />
							<attribute id="Version64" type="int64" value="36028797018963968" />
						</node>
					</children>
				</node>
			</children>
		</node>
	</region>
</save>
`

func modSettingsPath() string {
	return filepath.Join(modsDir(), "PlayerProfiles", "Public", "modsettings.lsx")
}

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func registerMods(info *modInfo) ([]string, error) {
	path := modSettingsPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		data = []byte(defaultModSettings)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		_ = os.WriteFile(path+".bak_"+time.Now().Format("20060102_150405"), data, 0o644)
	}
	text := string(data)
	var uuids []string
	for _, m := range info.Mods {
		if m.UUID == "" || strings.Contains(text, m.UUID) {
			continue
		}
		ver := m.Version64.String()
		if ver == "" {
			ver = m.Version.String()
		}
		if ver == "" {
			ver = "36028797018963968"
		}
		order := fmt.Sprintf("\n\t\t\t\t\t\t<node id=\"Module\">\n\t\t\t\t\t\t\t<attribute id=\"UUID\" type=\"FixedString\" value=\"%s\" />\n\t\t\t\t\t\t</node>", xmlEsc(m.UUID))
		desc := fmt.Sprintf("\n\t\t\t\t\t\t<node id=\"ModuleShortDesc\">\n"+
			"\t\t\t\t\t\t\t<attribute id=\"Folder\" type=\"LSString\" value=\"%s\" />\n"+
			"\t\t\t\t\t\t\t<attribute id=\"MD5\" type=\"LSString\" value=\"%s\" />\n"+
			"\t\t\t\t\t\t\t<attribute id=\"Name\" type=\"LSString\" value=\"%s\" />\n"+
			"\t\t\t\t\t\t\t<attribute id=\"UUID\" type=\"FixedString\" value=\"%s\" />\n"+
			"\t\t\t\t\t\t\t<attribute id=\"Version64\" type=\"int64\" value=\"%s\" />\n"+
			"\t\t\t\t\t\t</node>", xmlEsc(m.Folder), xmlEsc(m.MD5), xmlEsc(m.Name), xmlEsc(m.UUID), xmlEsc(ver))
		var ok1, ok2 bool
		text, ok1 = appendChild(text, "ModOrder", order)
		text, ok2 = appendChild(text, "Mods", desc)
		if !ok1 || !ok2 {
			return nil, errors.New("неожиданный формат modsettings.lsx")
		}
		uuids = append(uuids, m.UUID)
	}
	return uuids, os.WriteFile(path, []byte(text), 0o644)
}

func appendChild(text, nodeID, child string) (string, bool) {
	re := regexp.MustCompile(`(?s)(<node id="` + nodeID + `">\s*<children>)(.*?)(\s*</children>\s*</node>)`)
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return text, false
	}
	return text[:loc[6]] + child + text[loc[6]:], true
}

var moduleNodeRe = regexp.MustCompile(`(?s)\s*<node id="(?:Module|ModuleShortDesc)">(?:\s*<attribute[^>]*/>)*\s*</node>`)

func unregisterMods(uuids []string) error {
	path := modSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out := moduleNodeRe.ReplaceAllStringFunc(string(data), func(n string) string {
		for _, u := range uuids {
			if strings.Contains(n, `"`+u+`"`) {
				return ""
			}
		}
		return n
	})
	return os.WriteFile(path, []byte(out), 0o644)
}

// BG3 Russian — установщик русского языка для Baldur's Gate 3.
//
// Делает то же, что и ручная инструкция:
//  1. находит папку игры (Steam, GOG или путь, указанный пользователем);
//  2. переносит лишние файлы из Data\Localization в резервную папку
//     (оставляет English_Animations.pak, Language.lsx, Voice.pak, VoiceMeta.pak);
//  3. кладёт Russian.pak в Data\Localization\Russian;
//  4. меняет в Language.lsx значение English на Russian.
//
// Запуск с параметром --restore возвращает английский язык.
package main

import (
	"bufio"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

//go:embed Russian.pak
var russianPak []byte

const (
	gameDirName   = "Baldurs Gate 3"
	backupDirName = "_backup_bg3_rus"
)

// Файлы, которые инструкция велит оставить в Data\Localization.
var keepFiles = map[string]bool{
	"english_animations.pak": true,
	"language.lsx":           true,
	"voice.pak":              true,
	"voicemeta.pak":          true,
}

var stdin = bufio.NewReader(os.Stdin)

func main() {
	restore, chosen := false, false
	var userPath string
	for _, a := range os.Args[1:] {
		switch strings.ToLower(a) {
		case "--restore", "-restore", "/restore":
			restore, chosen = true, true
		case "--install", "-install", "/install":
			chosen = true
		default:
			userPath = a
		}
	}

	fmt.Println("=============================================")
	fmt.Println("  Baldur's Gate 3 — установка русского языка")
	fmt.Println("=============================================")
	fmt.Println()

	if !chosen {
		fmt.Println("1 — Установить русский язык")
		fmt.Println("2 — Вернуть английский язык")
		if ask("Ваш выбор [1]: ") == "2" {
			restore = true
		}
		fmt.Println()
	}

	err := run(userPath, restore)
	if err != nil {
		fmt.Println()
		fmt.Println("ОШИБКА:", err)
	}
	fmt.Println()
	ask("Нажмите Enter для выхода...")
	if err != nil {
		os.Exit(1)
	}
}

func run(userPath string, restore bool) error {
	locDir, err := findLocalization(userPath)
	if err != nil {
		return err
	}
	fmt.Println("Папка локализации:", locDir)

	if err := checkWritable(locDir); err != nil {
		if errors.Is(err, fs.ErrPermission) && relaunchAsAdmin(locDir, restore) {
			fmt.Println("Нет прав на запись — программа перезапущена от имени администратора.")
			return nil
		}
		return fmt.Errorf("нет доступа на запись в %s (%v).\nЗапустите программу от имени администратора", locDir, err)
	}

	if restore {
		return restoreEnglish(locDir)
	}
	return installRussian(locDir)
}

func installRussian(locDir string) error {
	backupDir := filepath.Join(locDir, backupDirName, time.Now().Format("2006-01-02_15-04-05"))

	// 1. Убираем лишнее в резервную копию (ничего не удаляем насовсем).
	entries, err := os.ReadDir(locDir)
	if err != nil {
		return err
	}
	moved := 0
	for _, e := range entries {
		name := e.Name()
		lower := strings.ToLower(name)
		if keepFiles[lower] || lower == "russian" || name == backupDirName {
			continue
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(locDir, name), filepath.Join(backupDir, name)); err != nil {
			return fmt.Errorf("не удалось перенести %s: %w", name, err)
		}
		fmt.Println("  перенесено в резервную копию:", name)
		moved++
	}
	if moved == 0 {
		fmt.Println("Лишних файлов в папке локализации нет.")
	} else {
		fmt.Println("Резервная копия:", backupDir)
	}

	// 2. Кладём Russian\Russian.pak.
	ruDir := filepath.Join(locDir, "Russian")
	if err := os.MkdirAll(ruDir, 0o755); err != nil {
		return err
	}
	pakPath := filepath.Join(ruDir, "Russian.pak")
	if err := os.WriteFile(pakPath, russianPak, 0o644); err != nil {
		return fmt.Errorf("не удалось записать %s: %w", pakPath, err)
	}
	fmt.Printf("Записан %s (%.1f МБ)\n", pakPath, float64(len(russianPak))/1024/1024)

	// 3. Меняем язык в Language.lsx.
	if err := setLanguage(locDir, "Russian"); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Готово! Запустите игру — интерфейс и субтитры будут на русском.")
	return nil
}

func restoreEnglish(locDir string) error {
	// Возвращаем файлы из резервных копий (сначала самые свежие).
	root := filepath.Join(locDir, backupDirName)
	backups, _ := os.ReadDir(root)
	for i := len(backups) - 1; i >= 0; i-- {
		dir := filepath.Join(root, backups[i].Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			dst := filepath.Join(locDir, f.Name())
			if _, err := os.Stat(dst); err == nil {
				continue
			}
			if err := os.Rename(filepath.Join(dir, f.Name()), dst); err != nil {
				return fmt.Errorf("не удалось вернуть %s: %w", f.Name(), err)
			}
			fmt.Println("  возвращено из резервной копии:", f.Name())
		}
		_ = os.Remove(dir)
	}
	_ = os.Remove(root)

	if err := setLanguage(locDir, "English"); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Язык переключён на английский.")
	fmt.Println("Если игра не запускается, проверьте целостность файлов в Steam:")
	fmt.Println("Библиотека → Baldur's Gate 3 → Свойства → Установленные файлы → Проверить целостность.")
	return nil
}

var (
	attrTagRe  = regexp.MustCompile(`<attribute\b[^>]*>`)
	idValueRe  = regexp.MustCompile(`\bid\s*=\s*"Value"`)
	valueAttRe = regexp.MustCompile(`(\bvalue\s*=\s*")[^"]*(")`)
)

const defaultLanguageLsx = `<?xml version="1.0" encoding="UTF-8"?>
<save>
	<version major="4" minor="0" revision="9" build="328" />
	<region id="Config">
		<node id="root">
			<attribute id="Value" value="%s" type="20" />
		</node>
	</region>
</save>
`

// setLanguage меняет value у <attribute id="Value" ...> в Language.lsx.
func setLanguage(locDir, lang string) error {
	path := filepath.Join(locDir, "Language.lsx")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Println("Language.lsx не найден — создаю новый.")
		return os.WriteFile(path, []byte(fmt.Sprintf(defaultLanguageLsx, lang)), 0o644)
	}
	if err != nil {
		return err
	}

	changed := false
	out := attrTagRe.ReplaceAllStringFunc(string(data), func(tag string) string {
		if changed || !idValueRe.MatchString(tag) {
			return tag
		}
		changed = true
		return valueAttRe.ReplaceAllString(tag, "${1}"+lang+"${2}")
	})
	if !changed {
		return fmt.Errorf("в %s не найдена строка <attribute id=\"Value\" ...>", path)
	}

	bak := path + ".bak"
	if _, err := os.Stat(bak); errors.Is(err, fs.ErrNotExist) {
		_ = os.WriteFile(bak, data, 0o644)
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return err
	}
	fmt.Printf("Language.lsx: язык установлен на %s\n", lang)
	return nil
}

// findLocalization ищет папку Data\Localization игры.
func findLocalization(userPath string) (string, error) {
	if userPath != "" {
		if dir := locFromPath(userPath); dir != "" {
			return dir, nil
		}
		fmt.Println("По указанному пути игра не найдена:", userPath)
	}

	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, wd)
	}
	for _, lib := range steamLibraries() {
		candidates = append(candidates, filepath.Join(lib, "steamapps", "common", gameDirName))
	}
	if runtime.GOOS == "windows" {
		for c := 'C'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			candidates = append(candidates,
				d+`Program Files (x86)\Steam\steamapps\common\`+gameDirName,
				d+`Program Files\Steam\steamapps\common\`+gameDirName,
				d+`Steam\steamapps\common\`+gameDirName,
				d+`SteamLibrary\steamapps\common\`+gameDirName,
				d+`Games\Steam\steamapps\common\`+gameDirName,
				d+`GOG Games\Baldurs Gate 3`,
				d+`Program Files (x86)\GOG Galaxy\Games\Baldurs Gate 3`,
				d+`Games\Baldurs Gate 3`,
			)
		}
	}
	for _, c := range candidates {
		if dir := locFromPath(c); dir != "" {
			return dir, nil
		}
	}

	fmt.Println("Не удалось автоматически найти игру.")
	for {
		p := ask(`Вставьте путь к папке игры (например C:\Program Files (x86)\Steam\steamapps\common\Baldurs Gate 3)` + "\nили пустую строку для выхода: ")
		if p == "" {
			return "", errors.New("папка игры не указана")
		}
		if dir := locFromPath(p); dir != "" {
			return dir, nil
		}
		fmt.Println("В этой папке нет Data\\Localization, попробуйте ещё раз.")
	}
}

// locFromPath принимает папку игры, папку Data или саму Localization.
func locFromPath(p string) string {
	p = strings.Trim(strings.TrimSpace(p), `"`)
	if p == "" {
		return ""
	}
	for _, c := range []string{
		filepath.Join(p, "Data", "Localization"),
		filepath.Join(p, "Localization"),
		p,
	} {
		if filepath.Base(c) != "Localization" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

var vdfPathRe = regexp.MustCompile(`"path"\s+"([^"]+)"`)

// steamLibraries возвращает все библиотеки Steam из реестра и libraryfolders.vdf.
func steamLibraries() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	var roots []string
	for _, q := range [][]string{
		{`HKCU\Software\Valve\Steam`, "SteamPath"},
		{`HKLM\SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
		{`HKLM\SOFTWARE\Valve\Steam`, "InstallPath"},
	} {
		out, err := exec.Command("reg", "query", q[0], "/v", q[1]).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if i := strings.Index(line, "REG_SZ"); i >= 0 {
				roots = append(roots, filepath.Clean(strings.TrimSpace(line[i+len("REG_SZ"):])))
			}
		}
	}

	libs := append([]string{}, roots...)
	for _, r := range roots {
		data, err := os.ReadFile(filepath.Join(r, "steamapps", "libraryfolders.vdf"))
		if err != nil {
			continue
		}
		for _, m := range vdfPathRe.FindAllStringSubmatch(string(data), -1) {
			libs = append(libs, strings.ReplaceAll(m[1], `\\`, `\`))
		}
	}
	return libs
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".bg3rus_*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// relaunchAsAdmin перезапускает программу с правами администратора (UAC).
func relaunchAsAdmin(locDir string, restore bool) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	psQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	args := psQuote(`"` + locDir + `"`)
	if restore {
		args += "," + psQuote("--restore")
	} else {
		args += "," + psQuote("--install")
	}
	ps := fmt.Sprintf("Start-Process -FilePath %s -ArgumentList %s -Verb RunAs", psQuote(exe), args)
	return exec.Command("powershell", "-NoProfile", "-Command", ps).Run() == nil
}

func ask(prompt string) string {
	fmt.Print(prompt)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

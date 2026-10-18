# Русский язык для Baldur's Gate 3

Программа `BG3_Russian_Installer.exe` сама делает всё, что описано в ручной инструкции по смене языка.

## Как пользоваться

1. Скачайте [`dist/BG3_Russian_Installer.exe`](dist/BG3_Russian_Installer.exe). Внутри уже есть `Russian.pak`, больше ничего качать не нужно.
2. Запустите его и выберите `1 — Установить русский язык`.
3. Если игра стоит в `Program Files`, Windows попросит права администратора. Согласитесь.
4. Запустите игру.

Чтобы вернуть английский, запустите программу и выберите `2`.

## Что делает программа

1. Ищет игру: в библиотеках Steam (через реестр и `libraryfolders.vdf`), в обычных папках Steam и GOG на всех дисках и в папке, где лежит сам exe. Если ничего не нашлось, попросит вставить путь вручную. Путь можно передать и первым аргументом.
2. Переносит в `Data\Localization\_backup_bg3_rus\<дата>` все файлы, кроме
   `English_Animations.pak`, `Language.lsx`, `Voice.pak` и `VoiceMeta.pak`.
   Файлы **не удаляются**, их можно вернуть.
3. Создаёт `Data\Localization\Russian\Russian.pak`.
4. Меняет в `Language.lsx` значение `value="English"` на `value="Russian"`. Исходный файл сохраняется как `Language.lsx.bak`.

При возврате английского (`2` или `--restore`) файлы из резервной копии возвращаются на место, а в `Language.lsx` снова прописывается `English`.

## Параметры командной строки

```
BG3_Russian_Installer.exe [путь к игре] [--install | --restore]
```

## Сборка

Нужен Go 1.21 или новее:

```
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o dist/BG3_Russian_Installer.exe .
```

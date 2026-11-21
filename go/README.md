# stock-client (Go)

Сборка: `./build.sh` (нужен Go ≥ 1.24; воспроизводимые файлы для Windows, macOS, Linux в `dist/`, суммы — `dist/SHA256SUMS`).
Тесты: `go test ./...`. Руководство для проверки кода: `AUDIT.md`.

Флаги: `-home <папка>` (где лежат `data/` и `secrets/`, по умолчанию — папка программы), `-listen 127.0.0.1:8240`,
`-no-browser`, `-no-scheduler`.

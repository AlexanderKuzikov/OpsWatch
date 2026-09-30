# OpsWatch — Instructions for AI Agents

## Commands
- build: `go build -ldflags="-s -w" -o opswatch ./cmd/opswatch`
- build linux: `$env:GOOS="linux"; $env:GOARCH="amd64"; go build -ldflags="-s -w" -o opswatch ./cmd/opswatch`
- vet: `go vet ./...` (проверять и с `GOOS=linux`, и с `GOOS=windows` — есть build-теги)
- test: `go test ./...`
- validate: `opswatch validate -registry config/registry.json`
- report: `opswatch report -registry config/registry.json -mail never`
- deploy: `scripts/install-sat.sh` на `sat` (идемпотентен)

## Conventions
- **Ноль зависимостей.** Только стандартная библиотека. Причина: бинарь живёт на
  VPS, где ничего доставить и пересобрать нельзя, а `go mod` без сети не работает.
- Реестр — **JSON**, не YAML: парсер YAML в стдлибе нет.
- Секретов в реестре нет. SMTP-пароль — из переменной окружения, имя которой
  лежит в `email.passEnvVar`. Не добавляй пароли в `config/registry.json`.
- Платформенные вызовы (statfs) — только через build-теги: `disk_unix.go` /
  `disk_windows.go`, общее объявление ошибки — в `probe.go`, иначе сборка ломается
  на одной из платформ.
- Статусы проверок: `FAIL` / `WARN` / `SKIP` / `OK`. `SKIP` — «здесь неприменимо»,
  **не** «сломано». Проверки, применимые только на сервере, гейтятся через
  `probe.IsServer()`.
- Валюты никогда не складываются. `registry.Cost` — карта по коду валюты.
- Код возврата `report`: `1`, если есть хоть одна сломанная проверка. systemd и
  cron полагаются на это, не разбирая вывод.

## Structure
- `cmd/opswatch/main.go` — подкоманды `report`, `heartbeat`, `validate`
- `internal/registry/` — типы реестра, валидация, расчёт списаний и сумм
- `internal/probe/` — HTTP, TLS, systemd, порты, диск, квота rclone
- `internal/report/` — markdown и тело письма
- `internal/alert/` — SMTP на `net/smtp`
- `config/registry.json` — единственный источник правды по платежам и адресам
- `scripts/install-sat.sh` — юниты и таймеры на sat

## Ловушки, уже оплаченные

1. **Расписание нельзя строить цепочкой.** `addMonths(prev, 1)` теряет исходный
   день: якорь 31-го клампится в февральское 28-е, и дальше март получается
   28-м. Каждое списание считается **от якоря** (`occurrence(n)`), клампapplied
   один раз. Тест `TestNextChargeMonthlyClampedDoesNotDrift` это стережёт.
2. **`systemctl show -p NRestarts` — счётчик за всю жизнь юнита**, авторестарты
   его не сбрасывают. Одно прочтение не отличает живую петлю от исторической:
   у `courtpulse` счётчик 10580, а юнит работает нормально. Нужны два замера с
   интервалом (`Snapshot` + `RestartLoop`).
3. **Юнит может быть `active` и при этом бесконечно перезапускаться.** Состояние
   `activating`, а не `failed`, поэтому в `systemctl --failed` его не видно.
4. **`scp` с Windows не ставит exec-бит.** После выкладки — `chmod 0755`, иначе
   `status=203/EXEC` и юнит в бесконечном рестарте.

## Do NOT touch
- `/etc/opswatch.env` — пароль SMTP, root 0600, вне репозитория
- `config/registry.json` — поправки сюда вносит **пользователь**, там его
  финансовая картина; агент только чинит формат и ошибки валидации
- Юниты `opswatch-*` на sat — правит `scripts/install-sat.sh`, не руками

## Documentation rules
- После работы — обнови `docs/CONTEXT.md`
- Новый провайдер или адрес в реестре — это не баг, а изменение данных;
  переспрашивай пользователя, а не додумывай суммы
- Переиспользуемые знания — в `D:\GitHub\knowledge/`
# OpsWatch — CONTEXT

> Последнее обновление: 2026-10-08 (балансы OpenRouter/RouterAI/Selectel + load/mem)

## Статус
| Компонент | Статус | Заметка |
|-----------|--------|---------|
| Реестр | Работает | `config/registry.json`, JSON без секретов, 5 платежей / 6 эндпоинтов / 6 TLS / 7 юнитов / 5 портов |
| Расчёт списаний | Работает | `monthly\|quarterly\|yearly\|oneoff\|usage`, кламп до последнего дня месяца, 8 тестов |
| Суммы | Работает | по валютам: **965 RUB/мес** (11 578/год) + **30 USD/мес** (360/год) |
| HTTP-пробы | Работает | 6 эндпоинтов, все 200 |
| TLS | Работает | 6 сертификатов, ближайший истекает через 41 день |
| systemd | Работает | 7 юнитов active |
| Петли рестартов | Работает | два замера с интервалом 20 с, historical ≠ текущий |
| Порты / диск / load / mem | Работает | только на сервере, с ноутбука `skip` |
| Балансы HTTP | **Готов код, ждёт ключи** | `via: openrouter\|routerai\|selectel`, секреты в env, пустой ключ = WARN; 8 тестов на httptest |
| Баланс VseLLM | Ручной | API баланса у VseLLM нет (404) — `known` + `lastUpdate`, протухание 30 дней = WARN |
| Отчёт markdown | Работает | `last-report.md`, тело алерта |
| Отчёт HTML | Работает | `last-report.html`, один файл, CSS встроен, без CDN, тёмная тема по `prefers-color-scheme` |
| Квоты rclone | **Ждёт** | `mailru:` не настроен — нужен пароль приложения mail.ru |
| Почта | **Ждёт** | `/etc/opswatch.env` создан, `OPSWATCH_SMTP_PASS` пуст |
| Таймеры | Работают | `opswatch-report` 15 мин (пишет md + html), `opswatch-heartbeat` ежедневно, оба active |

## Open-проблемы
| # | Priority | Описание |
|---|----------|----------|
| 1 | high | Не настроен remote `mailru:` — проверка квоты Облака падает с `exit status 1`. Нужен пароль приложения mail.ru с правами «Full access to Mail, Cloud and Calendar» |
| 2 | high | `/etc/opswatch.env` создан, но `OPSWATCH_SMTP_PASS` пуст — алерты и heartbeat молчат. Нужен Gmail app password |
| 3 | medium | Ручные балансы без API: OpenRouter/RouterAI/Selectel теперь читаются живьём по `via`, остался только VseLLM (API баланса нет, только кабинет) |
| 4 | medium | `courtpulse.service` имеет 10 580 рестартов за всю жизнь при нормальной работе. Историческая грязь; лечится `systemctl reset-failed`, но это чужой проект |
| 5 | low | Нет проверки «прошло ли N часов с последнего успешного отчёта» — если таймер сломается, заметить некому, кроме как по отсутствию писем |
| 6 | low | Нет проверки возраста TLS для `nip.io` как класса: при смене IP Selectel все адреса меняются разом |
| 7 | low | DNS-резолв адресов не проверяется — только HTTP и сертификат. Упавший `A`-запись даст `FAIL` в HTTP, но без указания на DNS |

## Что показал первый же прогон
- `casecore.*` отдавал `tls: internal error` — оказалось не поломкой, а моим
  устаревшим реестром: витрину переименовали в `casecore-frozen.*`. Реестр исправлен.
- `courtpulse.service` — 10 580 рестартов. Проверка оказалась негодной: `NRestarts`
  кумулятивный и не сбрасывается авторестартами. Переделана на два замера.
- `seogladys.service` (погашен 30.09, см. SerpGladys) — тот же класс петли.

## Мои ошибки, пойманные тестами и проверками
| Где | Что было | Как поймал |
|-----|----------|-----------|
| `forecast.Upcoming` | Списание считалось цепочкой `addMonths(prev, 1)`: якорь 31-го клампился в февральское 28-е, дальше март выходил 28-м. Подписка уползала бы на день каждые пару месяцев | `TestNextChargeMonthlyClampedDoesNotDrift` |
| `report.Markdown` | Формат `%s %.0f %s` печатал валюту дважды, и валюты складывались в одну сумму: «995 ₽/мес» с долларами внутри | ручной прогон отчёта |
| `probe.NRestarts` | Одно прочтение кумулятивного счётчика дало ложную тревогу на `courtpulse` (10 580 рестартов при штатной работе) | прогон на самом VPS |
| Хост-гейт | Первая версия показывала 14 ошибок при запуске с ноутбука: systemd-пробы исполнялись на машине пользователя, здоровый VPS выглядел мёртвым | прогон с ноутбука |
| `.gitignore` | Строка `opswatch` глушила не только собранный бинарь, но и всю папку исходников: главный файл три коммита лежал вне git | `git status` после правок — главного файла нет в списке |
| SerpGladys `writeColdMonth` | Заменил `cw.Flush()` на `cw.Error()`, а `Error()` буфер не выталкивает — архивы получались пустыми | round-trip тест холодного яруса |
| SerpGladys `ProjectSizes` | Ключи вида `zavodsvay_50-desktop` вместо `zavodsvay` | разбор живого ответа API |

## Журнал работ
| Дата | Изменение |
|------|-----------|
| 2026-10-08 | **Живые балансы + load/mem.** Новый seam `via` у баланса: адаптеры `openrouter` (credits с fallback на auth/key), `routerai` (credits), `selectel` (balances + prediction), ручной с правилом протухания 30 дней. VseLLM проверен — API баланса нет, только ручной. Пустой ключ = WARN. Пробы `load`/`mem` через /proc (только Linux). 8 тестов на httptest без сети. Реестр не тронут — записи и пороги вносит пользователь |
| 2026-10-01 02:50 | **HTML-отчёт.** `-format html` рендерит `internal/report/html.go`: `html/template` (инъекции в notes не проходят), CSS встроен в файл, ни одного внешнего ресурса — открывается с `file://`, тёмная тема через `prefers-color-scheme`, favicon как data-URI чтобы не было 404. Таймер `opswatch-report` пишет обе версии: `.md` для истории, `.html` для просмотра. Проверено рендером в браузере: 33 проверки, сортировка по критичности, единственный `FAIL` — ненастроенный `mailru:` |
| 2026-09-30 23:30 | **v0.1 создан и задеплоен на sat.** Реестр с раздельными валютами, расчёт списаний с клампом месяца, 6 HTTP-проб, 6 TLS-пров, 7 юнитов, 5 портов, квота rclone, SMTP на `net/smtp` без зависимостей, `report`/`heartbeat`/`validate`, таймеры 15 мин + ежедневно, `scripts/install-sat.sh`. 8 тестов на расчёт списаний |

## Структура
```
OpsWatch/
├── cmd/opswatch/main.go      — report | heartbeat | validate
├── internal/registry/        — типы, валидация, расчёт списаний и сумм
├── internal/probe/           — HTTP, TLS, systemd, порты, диск, rclone about
├── internal/report/          — markdown, HTML, тело письма
├── internal/alert/           — SMTP
├── config/registry.json      — источник правды
└── scripts/install-sat.sh    — юниты + таймеры
```

## Верификация
```bash
go vet ./... && go test ./...
GOOS=linux go vet ./... && GOOS=linux go build ./cmd/opswatch
opswatch validate -registry config/registry.json
opswatch report -registry config/registry.json -mail never -data-dir /
opswatch report -registry config/registry.json -mail never -format html -out last-report.html
```
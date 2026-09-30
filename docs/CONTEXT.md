# OpsWatch — CONTEXT

> Последнее обновление: 2026-09-30 23:30 MSK

## Статус
| Компонент | Статус | Заметка |
|-----------|--------|---------|
| Реестр | Работает | `config/registry.json`, JSON без секретов, 5 платежей / 6 эндпоинтов / 6 TLS / 7 юнитов / 5 портов |
| Расчёт списаний | Работает | `monthly\|quarterly\|yearly\|oneoff\|usage`, кламп до последнего дня месяца, 8 тестов |
| Суммы | Работает | по валютам: **965 RUB/мес** (11 578/год) + **30 USD/мес** (360/год) |
| HTTP-пробы | Работает | 6 эндпоинтов, все 200 |
| TLS | Раработает | 6 сертификатов, ближайший истекает через 49 дней |
| systemd | Работает | 7 юнитов active |
| Петли рестартов | Работает | два замера с интервалом 20 с, historical ≠ текущий |
| Порты / диск | Работает | только на сервере, с ноутбука `skip` |
| Квоты rclone | **Ждёт** | `mailru:` не настроен — нужен пароль приложения mail.ru |
| Почта | **Ждёт** | `/etc/opswatch.env` создан, `OPSWATCH_SMTP_PASS` пуст |
| Таймеры | Работают | `opswatch-report` 15 мин, `opswatch-heartbeat` ежедневно, оба active |

## Open-проблемы
| # | Priority | Описание |
|---|----------|----------|
| 1 | high | Не настроен remote `mailru:` — проверка квоты Облака падает с `exit status 1`. Нужен пароль приложения mail.ru с правами «Full access to Mail, Cloud and Calendar» |
| 2 | high | `/etc/opswatch.env` создан, но `OPSWATCH_SMTP_PASS` пуст — алерты и heartbeat молчат. Gmail app password |
| 3 | medium | Балансы без API (`AI Studio`, `OpenRouter`) висят как `known: 0` и требуют ручного обновления в реестре. Автоматизировать нечем — API остатка нет |
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

## Журнал работ
| Дата | Изменение |
|------|-----------|
| 2026-09-30 23:30 | **v0.1 создан и задеплоен на sat.** Реестр с раздельными валютами, расчёт списаний с клампом месяца, 6 HTTP-проб, 6 TLS-пров, 7 юнитов, 5 портов, квота rclone, SMTP на `net/smtp` без зависимостей, `report`/`heartbeat`/`validate`, таймеры 15 мин + ежедневно, `scripts/install-sat.sh`. 8 тестов на расчёт списаний. Два теста поймали мой баг: `cw`-подобная потеря якоря в `Upcoming` и переставленная валюта в таблице списаний |

## Структура
```
OpsWatch/
├── cmd/opswatch/main.go      — report | heartbeat | validate
├── internal/registry/        — типы, валидация, расчёт списаний и сумм
├── internal/probe/           — HTTP, TLS, systemd, порты, диск, rclone about
├── internal/report/          — markdown + тело письма
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
```
# OpsWatch — сторож платежей, сервера и подписок

Один бинарь на Go без единой зависимости. Знает три вещи:

1. **Когда и сколько спишут** — реестр платежей, прогноз по якорной дате и периоду.
2. **Что сломано** — HTTP-эндпоинты, systemd-юниты, порты, диск, TLS-сертификаты, квоты.
3. **Жив ли сам сервер** — ежедневное письмо-пинг: нет письма = хост умер.

## Почему так

Мониторинг, который живёт на том же сервере, который проверяет, не заметит смерти
сервера. Поэтому здесь два независимых механизма: пробы живут на сервере, а
heartbeat-письмо отправляется **наружу** — его отсутствие и есть сигнал.

Расходы разводятся по валютам. Складывать рубли с долларами — значит получить
число, которое ничего не означает.

## Команды

```bash
opswatch validate  -registry config/registry.json   # только разбор и проверка
opswatch report    -registry config/registry.json   # все проверки + отчёт
opswatch report    -mail always                     # слать всегда, не только при проблемах
opswatch report    -mail never -quiet               # без почты, без stdout
opswatch heartbeat                                 # одно письмо «я жив»
```

Коды возврата: `0` — всё зелёное или только предупреждения, `1` — есть сломанная
проверка. `systemd` и cron видят это сами.

## Флаги `report`

| Флаг | По умолчанию | Смысл |
|---|---|---|
| `-registry` | `config/registry.json` или `$OPSWATCH_REGISTRY` | файл реестра |
| `-out` | — | записать markdown-отчёт в файл |
| `-mail` | `problems` | `always` \| `never` \| `problems` |
| `-quiet` | false | не печатать в stdout |
| `-data-dir` | `/` | какая ФС показывать в `disk` |
| `-restart-window` | `20s` | окно замера для детекта петли рестартов |

## Что внутри реестра

`config/registry.json` — единственный источник правды. Секретов в нём нет:
SMTP-пароль читается из переменной, имя которой указано в `email.passEnvVar`.

```jsonc
{
  "warnChargeInDays": 7,
  "payments":     [{ "id", "provider", "name", "amount", "currency",
                     "period": "monthly|quarterly|yearly|oneoff|usage",
                     "anchor": "YYYY-MM-DD", "method", "autoRenew", "active" }],
  "domains":      [{ "host", "renewal", "registrar", "notes" }],
  "endpoints":    [{ "name", "url", "expectStatus", "bodyContains", "headers" }],
  "tls":          [{ "host", "warnBeforeDays" }],
  "balances":     [{ "id", "provider", "known", "warnBelow", "viaRclone" }],
  "units":        [{ "unit", "why" }],
  "ports":        [{ "port", "why" }],
  "email":        { "host", "port", "user", "from", "to", "passEnvVar", "useTLS" }
}
```

`period: "usage"` — pay-as-you-go. Фиксированного списания нет, такая запись не
попадает ни в прогноз, ни в суммы, но видна как «следить за балансом вручную».

`anchor` — дата **известного** списания. Дальше считается вперёд: помесячно с
клампом до последнего дня месяца, поквартально, по yearly.

## Установка на sat

```bash
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o opswatch ./cmd/opswatch
scp -i C:\Users\alexa\.ssh\id_ed25519 opswatch deploy@135.106.192.125:/tmp/opswatch
scp -i C:\Users\alexa\.ssh\id_ed25519 config/registry.json deploy@135.106.192.125:/home/deploy/opswatch/config/
scp -i C:\Users\alexa\.ssh\id_ed25519 scripts/install-sat.sh deploy@135.106.192.125:/tmp/
ssh -i C:\Users\alexa\.ssh\id_ed25519 deploy@135.106.192.125
sudo install -m 0755 /tmp/opswatch /usr/local/bin/opswatch
bash /tmp/install-sat.sh
```

Скрипт идемпотентен. Создаёт `opswatch-report.timer` (каждые 15 мин) и
`opswatch-heartbeat.timer` (ежедневно), плюс `/etc/opswatch.env`.

**Пароль от почты** вписывается один раз руками, в репозиторий не попадает:

```bash
sudo nano /etc/opswatch.env     # OPSWATCH_SMTP_PASS=app-password
sudo systemctl restart opswatch-report.service
```

App password для Gmail: <https://myaccount.google.com/apppasswords> (нужен 2FA).
До его заполнения отчёт считается, но письма не уходят — в журнале будет
`env OPSWATCH_SMTP_PASS is empty`.

## Хост-локальные проверки

Юниты, порты и файловая система имеют смысл только на самом сервере. Запуск с
ноутбука не превратит здоровый VPS в стену ложных падений: при отсутствии
`systemctl` эти проверки помечаются `skip`, а не `FAIL`.

## Детект петли рестартов

`NRestarts` в systemd — счётчик за всю жизнь юнита, и **автоматические рестарты
его не сбрасывают**. Поэтому одно прочтение ничего не говорит о текущем
состоянии: у `courtpulse.service` счётчик 10580, при этом юнит стабильно работает.

Поэтому счётчик читается дважды с интервалом `-restart-window`. Растущий
счётчик — живая петля (5+ рестартов за окно = `FAIL`). Именно так на 30.09 был
найден `seogladys.service`, который месяц висел в рестарт-петле с
`Restart=always` после переименования каталога.

У таймеров и сокетов такого счётчика нет — они помечаются `skip`.

## Проверки

```bash
go vet ./...
go test ./...
```

Тесты покрывают расчёт списаний: кламп 31-го на короткие месяцы (иначе дата
уползает на день каждые пару месяцев), 29 февраля, границы окна предупреждения,
разделение валют. Ветку «почта недоступна» тестами не покрыть — она упирается в
сеть; вместо неё ошибка возвращается наружу и попадает в unit-лог.
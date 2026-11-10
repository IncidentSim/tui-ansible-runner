# incidentctl

`incidentctl` запускает учебные incident exercises на заранее подготовленной Linux VM. CLI работает на локальной машине, загружает выбранный каталог из `exersice/` по SSH и выполняет Ansible на самой VM с inventory `localhost`.

Firecracker, KVM, TAP-интерфейсы и вложенные гостевые VM не используются.

## Требования

Локально:

- Go 1.26;
- OpenSSH client;
- SSH-ключ для целевой VM.

На VM:

- Linux и доступ по SSH;
- `bash`, `python3`, `tar`, `ansible-playbook`;
- пользователь, имеющий права, необходимые playbook'ам. Текущая конфигурация использует `root`.

Требования конкретного упражнения, включая зафиксированные версии пакетов, описаны в его `vm.json`.

## Настройка

```bash
cp incidentctl.example.json incidentctl.json
go build -o bin/incidentctl ./cmd/incidentctl
```

`incidentctl.json` содержит локальный путь к SSH-ключу и не попадает в Git.

### Подготовка Ubuntu VM

Проект проверен на Ubuntu 24.04.5 LTS. На чистой VM до первого запуска `incidentctl` должны быть установлены:

| Пакет | Проверенная версия | Назначение |
|---|---|---|
| `ansible-core` | `2.16.3-0ubuntu2` | Запуск playbook'ов на VM |
| `python3` | `3.12.3-0ubuntu2.1` | Runtime Ansible и служебных модулей |
| `python3-apt` | `2.7.7ubuntu5.3` | Работа Ansible с APT |
| `tar` | `1.35+dfsg-3ubuntu0.4` | Распаковка загруженного упражнения |
| `ca-certificates` | `20260816ubuntu0.24.04.2` | Проверка TLS-сертификатов при загрузке пакетов и образов |

Установить полный bootstrap-набор можно так:

```bash
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y \
  ansible-core=2.16.3-0ubuntu2 \
  python3=3.12.3-0ubuntu2.1 \
  python3-apt=2.7.7ubuntu5.3 \
  tar=1.35+dfsg-3ubuntu0.4 \
  ca-certificates=20260816ubuntu0.24.04.2
```

Для обычной Ubuntu VM, где SSH не предоставляется гипервизором, дополнительно установите и запустите SSH-сервер:

```bash
DEBIAN_FRONTEND=noninteractive apt-get install -y openssh-server
systemctl enable --now ssh
```

Также требуется пользователь с доступом по SSH и правами на изменение системной конфигурации. Текущий пример подключается как `root`; при использовании непривилегированного пользователя playbook'ам потребуется настроенный `sudo`.

Текущая OrbStack VM настраивается так:

```bash
ssh root@incidentsim.orb.local -i ~/.orbstack/ssh/id_ed25519
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get install -y ansible-core=2.16.3-0ubuntu2
```

Docker, Nginx и приложения устанавливаются infra-playbook'ом, а не вручную.

Зафиксированные версии относятся к настроенным Ubuntu-репозиториям на момент проверки. Если репозиторий уже не содержит конкретную версию, сначала определите новый candidate через `apt-cache policy <package>`, затем синхронно обновите `vm.json`, infra-playbook и эту таблицу.

## Команды

```bash
# Открыть интерактивный TUI (режим по умолчанию)
bin/incidentctl

# Проверить SSH и базовое окружение VM
bin/incidentctl ssh-check

# Получить hostname, IP, порт и реквизиты входа
bin/incidentctl connection

# Развернуть или восстановить исправную инфраструктуру
bin/incidentctl infra nginx-wrong-api

# Внести поломку
bin/incidentctl incident nginx-wrong-api

# Проверить пользовательский сценарий; ненулевой код означает провал проверки
bin/incidentctl verify nginx-wrong-api
```

## Интерактивный режим

TUI показывает список упражнений, их описания и подсказки. При запуске решения
он последовательно выполняет `infra`, `incident` и `verify`. Задача считается
готовой только когда проверка после внесения инцидента завершается с ненулевым
кодом. Если verification неожиданно проходит или SSH/загрузка завершаются
ошибкой, запуск решения останавливается.

На экране активного решения доступны:

- просмотр описания и подсказок;
- показ готовой SSH-команды;
- открытие SSH-сессии в отдельном окне Terminal (macOS), поддерживаемого
  эмулятора терминала (Linux) или `cmd` (Windows);
- запуск verification для проверки исправления;
- выход к списку задач без изменения текущего состояния VM.

Управление всегда показано в нижней строке текущего экрана. Для аварийного
выхода используйте `Ctrl+C`.

Дополнительные аргументы после `--` передаются `ansible-playbook`:

```bash
bin/incidentctl infra nginx-wrong-api -- --check --diff
```

## Структура упражнения

```text
exersice/<name>/
├── README.md
├── vm.json
├── apps/
├── infra/
│   ├── playbook.yml
│   └── monitoring/
├── incident/
│   └── playbook.yml
├── verification/
│   └── playbook.yml
└── tips/
```

- `infra` идемпотентно создаёт исправное состояние.
- `incident` вносит только целевую неисправность.
- `verification` проверяет наблюдаемое поведение и завершается с ошибкой при активном инциденте.
- повторный запуск `infra` восстанавливает исправное состояние.

Написание `exersice` сохранено как существующий контракт проекта.

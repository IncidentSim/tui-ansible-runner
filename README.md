# incidentctl

`incidentctl` получает каталог incident exercises из настроенного удалённого
Git-репозитория и обращается к VM service API. Локальный каталог заданий и
подключение к локальной VM больше не являются вариантами запуска.

Сейчас API-провайдер оставлен заглушкой: список заданий можно получить из
удалённого репозитория, но операции `infra`, `incident`, `verify`, `connection`
и `ssh-check` завершатся ошибкой `microVM service provider is not implemented`,
пока API-клиент не будет реализован.

## Требования

Локально:

- Go 1.26;
- Git;
- доступ к настроенному удалённому Git-репозиторию;
- конфигурация VM service API.

Требования конкретного упражнения, включая ресурсы VM и версии пакетов,
описаны в его `vm.json` и передаются будущему API-провайдеру.

## Настройка

```bash
cp incidentctl.example.json incidentctl.json
go build -o bin/incidentctl ./cmd/incidentctl
```

Те же действия доступны через `Makefile`:

```bash
make config   # создать incidentctl.json, если его ещё нет
make build    # собрать bin/incidentctl
make run      # собрать и запустить TUI
make test
make check    # форматирование, тесты, vet и сборка
```

Дополнительные аргументы CLI можно передать через `ARGS`, например:

```bash
make run ARGS='infra nginx-wrong-api'
```

`incidentctl.json` объединяет настройки удалённого Git-репозитория и VM service
API. Секция `exercise_repository` задаёт repository, ref, каталог с
упражнениями и локальный кеш checkout. Кеш нужен только как техническое место
для чтения уже загруженного удалённого репозитория; источником задач остаётся
только настроенный remote.

При первом обычном запуске создаётся случайный UUID пользователя устройства и
сохраняется в системном каталоге конфигурации ОС:
`<user-config-dir>/incidentctl/user-id`. Файл имеет права `0600`, а один и тот
же ID используется при последующих запусках. Путь можно переопределить полем
`user_id_file` в `incidentctl.json`; этот ID будет передаваться будущему API
как `user_id`.

```bash
bin/incidentctl -config incidentctl.json
```

Флаг имеет это значение по умолчанию, поэтому обычно достаточно:

```bash
bin/incidentctl
```

В секции `vm_service` токен не хранится: поле `token_env` содержит имя
переменной окружения, из которой будущая реализация сможет получить токен.

### Репозиторий упражнений

Удалённый источник работает с обычным Git-репозиторием и не привязан к GitHub
API. При запуске он получает указанный `ref` в каталог кеша, затем читает
упражнения из `subdirectory`.

Шаблон уже настроен на `git@github.com:IncidentSim/exersice.git`, ветку `master`
и корень репозитория. Формат репозитория описан в `exersice/README.md`.

Чтобы опубликовать изменения каталога из отдельного checkout:

```bash
cd exersice
git switch master
git pull --ff-only
# изменить задания, skills или README
git add .
git commit -m "Describe exercise update"
git push origin master
cd ..
```

### API VM service

Провайдер выбирается автоматически и всегда является `microvm-service`. Его
настройки находятся в секции `vm_service` единого `incidentctl.json`:

```json
"vm_service": {
  "base_url": "https://microvm.example/api",
  "token_env": "INCIDENTCTL_MICROVM_TOKEN",
  "timeout_seconds": 30,
  "remote_work_dir": "/var/tmp/incidentctl/exercises"
}
```

HTTP-вызовы API пока не выполняются — провайдер намеренно возвращает явную
ошибку-заглушку. Это оставляет единый контракт для будущего создания VM через
сервис и исключает локальное развёртывание.

## Команды

```bash
# Открыть TUI; список загружается из удалённого Git-репозитория
bin/incidentctl

# Будущие операции через API
bin/incidentctl infra nginx-wrong-api
bin/incidentctl incident nginx-wrong-api
bin/incidentctl verify nginx-wrong-api
```

Доступные флаги:

```text
-config incidentctl.json
```

Аргументы после `--` сохраняются для будущей передачи удалённому исполнителю
playbook'ов.

## Структура упражнения

```text
<remote-repository>/<name>/
├── README.md
├── vm.json
├── apps/
├── infra/
├── incident/
├── verification/
└── tips/
```

`infra` создаёт исправное состояние, `incident` вносит целевую неисправность,
а `verification` проверяет наблюдаемое поведение. Написание `exersice`
сохранено как существующий контракт проекта.

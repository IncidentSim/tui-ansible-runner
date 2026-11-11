# incidentctl

`incidentctl` получает каталог incident exercises из настроенного удалённого
Git-репозитория и обращается к VM service API. Локальный каталог заданий и
подключение к локальной VM больше не являются вариантами запуска.

TUI использует VM service API: при запуске проверяется `/v1/capabilities`, а
при начале решения создаётся VM, после чего `/v1/vms/{id}/terminal` выдаёт URL
веб-терминала, который открывается в браузере. После готовности SSH-клиент
проверяет и при необходимости устанавливает bootstrap-пакеты упражнения,
загружает exercise, запускает playbooks `infra` и `incident`, а затем выполняет
скрытую автоматическую verification-проверку ожидаемой ошибки. Только после
успешного подтверждения открывается веб-терминал.
SSH-код и проверка результата сохранены для совместимости, но их кнопки убраны
из TUI.

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

Токен вводится в TUI клавишей `a` на экране списка или описания задачи. Он
сохраняется рядом с `user-id` в `token.json` с полями `token` и
`installed_at` — Unix timestamp установки. Отсутствующий или некорректный
токен не мешает загрузить список задач и выбрать задачу; он проверяется только
при работе с VM service.

```bash
bin/incidentctl -config incidentctl.json
```

Флаг имеет это значение по умолчанию, поэтому обычно достаточно:

```bash
bin/incidentctl
```

В секции `vm_service` токен не хранится: он вводится через TUI и сохраняется
в системном файле рядом с `user-id`.

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
  "base_url": "https://petruekhin.tech:19453",
  "timeout_seconds": 30,
  "remote_work_dir": "/var/tmp/incidentctl/exercises",
  "image_id": "ubuntu-base",
  "ttl_seconds": 3600
}
```

Из OpenAPI сервиса используются `GET /v1/capabilities`, `GET /v1/vms`,
`POST /v1/vms`, `DELETE /v1/vms/{id}`, `GET /v1/operations/{id}`,
`POST /v1/vms/{id}/terminal`,
`GET /v1/vms/{id}/ssh` и `GET /v1/vms/{id}/ssh-key`. Мутации возвращают
операции, поэтому клиент дожидается статуса `succeeded`; при выходе из TUI
клавишей `q` все VM пользователя сначала запрашиваются, затем удаляются.
SSH-метаданные опрашиваются до `ready=true`. При HTTP 401 TUI
сообщает, что токен просрочен, а ошибка создания VM показывается как:
«сейчас сервер занят, попробуйте подключиться позже».

`image_id` обязателен для создания VM. Токен передаётся в API как
`Authorization: Bearer <token>`; для пользовательского JWT заголовок
`X-Acting-User-ID` не добавляется согласно OpenAPI.

## Команды

```bash
# Открыть TUI; список загружается из удалённого Git-репозитория
bin/incidentctl

# Legacy SSH-команды остаются доступны вне TUI
bin/incidentctl infra nginx-wrong-api
bin/incidentctl incident nginx-wrong-api
bin/incidentctl verify nginx-wrong-api
```

Доступные флаги:

```text
-config incidentctl.json
```

Аргументы после `--` сохраняются для передачи удалённому исполнителю
playbook'ов в legacy SSH-потоке.

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

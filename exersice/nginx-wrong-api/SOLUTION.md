# Решение инцидента «Не тот адрес»

## Краткий ответ

Nginx заменяет входящий заголовок `Host` адресом upstream-сервиса из-за директивы:

```nginx
proxy_set_header Host $proxy_host;
```

Для этого задания её нужно заменить на передачу исходного заголовка клиента:

```nginx
proxy_set_header Host $http_host;
```

## Воспроизведение

Внести инцидент и запустить проверку:

```bash
bin/incidentctl incident nginx-wrong-api
bin/incidentctl verify nginx-wrong-api
```

Проверка завершается с ошибкой и показывает различие:

```text
Expected verification.incidentsim.test,
but the echo-server received 127.0.0.1:8080.
```

Сервис при этом доступен и отвечает HTTP 200. Ошибка находится именно в переданном значении `Host`, а не в доступности Nginx или контейнера.

## Диагностика

На VM можно воспроизвести симптом отдельным запросом:

```bash
curl --silent \
  --header 'Host: api.example.test' \
  http://127.0.0.1/
```

При активном инциденте ответ выглядит так:

```json
{"host":"127.0.0.1:8080"}
```

Echo-server только возвращает полученный заголовок, поэтому следующий участок для проверки — reverse proxy. Найти активную директиву можно так:

```bash
nginx -T 2>/dev/null | grep 'proxy_set_header Host'
```

Результат:

```nginx
proxy_set_header Host $proxy_host;
```

Переменная `$proxy_host` содержит имя и порт upstream из `proxy_pass`. В этом упражнении upstream задан как `http://127.0.0.1:8080`, поэтому именно это значение получает приложение. Переменная `$http_host` содержит исходное значение HTTP-заголовка `Host`, переданного клиентом.

## Исправление вручную

Откройте `/etc/nginx/sites-available/incidentsim` и замените:

```nginx
proxy_set_header Host $proxy_host;
```

на:

```nginx
proxy_set_header Host $http_host;
```

После изменения проверьте конфигурацию и примените её:

```bash
nginx -t
systemctl reload nginx
```

Не следует исправлять echo-server: изменение приложения скроет ошибку reverse proxy и нарушит заданный контракт.

## Восстановление через исходный код упражнения

Предпочтительный воспроизводимый способ — повторно применить infra-playbook, для которого исправная конфигурация является source of truth:

```bash
bin/incidentctl infra nginx-wrong-api
```

Он восстановит директиву `proxy_set_header Host $http_host;`, проверит конфигурацию Nginx и перезапустит сервис только при наличии изменений.

## Проверка решения

```bash
bin/incidentctl verify nginx-wrong-api
```

Ожидаемый результат:

```text
The original Host header reached the echo-server.
```

Повторный запрос также возвращает исходный адрес:

```bash
curl --silent \
  --header 'Host: api.example.test' \
  http://127.0.0.1/
```

```json
{"host":"api.example.test"}
```

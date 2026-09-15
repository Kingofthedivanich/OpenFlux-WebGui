# OpenFlux

[English](README.md) | **Русский**

Исследовательский инструмент сетевого стека. TCP-туннель с подключаемыми
транспортами, батчированным zstd-кодеком и L3-режимом выходной ноды.

# Отказ от ответственности

Автор OpenFlux **не призывает** использовать данный проект для обхода
блокировок или нарушения правил каких-либо платформ, а также **не несёт
ответственности** за финальные сценарии использования утилиты пользователями
в реальной жизни или сети Интернет. Любые специфические технические
особенности приложения - не более чем **архитектурное совпадение**, созданное
**без какого-либо умысла**.

Проект является **полностью некоммерческим**, не содержит **платных функций,
скрытых подписок или коммерческой выгоды**.

Автор **не несёт ответственности** за форки, модификации и производные
версии OpenFlux, созданные третьими лицами. Любые изменения, добавленные
в форк, являются ответственностью его автора.

Автор **не несёт ответственности** за:

- Любое использование OpenFlux третьими лицами
- Последствия, вызванные использованием форков и модификаций
- Ущерб, возникший в результате работы производных версий
- Нарушения, совершённые с использованием форков

Оригинальный код предоставляется **как есть** («as is»), **без каких-либо
гарантий**.

## Клиенты

| Платформа | Скачать | Примечания |
|-----------|---------|------------|
| **macOS**   | сборка из исходников | CLI + utun L3-клиент (--tun) |
| **Linux**   | сборка из исходников | CLI-клиент / выходная нода |
| **Windows** | сборка из исходников | CLI-клиент / выходная нода (proxy) |
| **Android** | [Релизы OpenFluxAndroid](https://github.com/p1neappleXpress/OpenFluxAndroid) | Отдельный APK |
| **iOS**     | [TestFlight бета](https://testflight.apple.com/join/BwnAcdus) | Системный VPN через Network Extension |

> **iOS-приложение** сделано [@saharev1](https://github.com/saharev1) -
> полноценный iOS-клиент, пайплайн TestFlight, системный VPN, DNS-over-TLS
> и множество фиксов стабильности. ОГРОМНОЕ спасибо!
>
> **Android-приложение** - [p1neappleXpress/OpenFluxAndroid](https://github.com/p1neappleXpress/OpenFluxAndroid).

## Архитектура

macOS client (utun)    --> Транспорт --> Выходная нода (L3) --> Интернет
Linux/Windows client   --> Транспорт --> Выходная нода (L3) --> Интернет
iOS packet tunnel      --> Транспорт --> Выходная нода (L3) --> Интернет
Android client         --> Транспорт --> Выходная нода (L3) --> Интернет

Выходная нода ничего не терминирует: она форвардит сырые IP-пакеты с
SNAT/DNAT (conntrack + фильтр по egress-IP). Одно TCP-соединение end-to-end
между клиентом и реальным сервером.

Клиент терминирует TCP локально (gVisor, utun или NEPacketTunnelProvider),
отправляет сырые IP-пакеты в транспорт. Выходная нода переписывает
src/dst-адреса и форвардит - TCP-состояние она не видит никогда.

## Ключевые особенности

- **Подключаемые транспорты** - Yandex.Docs (WS), Yandex Volga (HTTP relay),
  MAX/OneMe (WebRTC DataChannel), Cups.online (Centrifugo-комнаты).
- **Батчинг + zstd** - склеивает множество туннельных пакетов в одно
  транспортное сообщение. Меньше сообщений в канале, выше скорость. См.
  transport/batched.go и transport/framing.go.
- **L3-выход** - нода в режиме --mode l3 форвардит сырые IPv4-пакеты через
  SOCK_RAW (Linux) или WinDivert (Windows). Без userspace TCP-стека, без
  двойной терминации.
- **macOS utun-клиент** - --client --tun (только macOS). Создаёт utun-
  интерфейс, следит за своими сокетами и ставит bypass-маршруты, затем
  забирает default-маршрут. Никакого SOCKS5, никакого gVisor.
- **Legacy fallback** - --legacy возвращает транспорт к старому кодеку
  с per-packet LZ4 (совместим со старыми клиентами).
- **Режимы бенчмарка** - --bench-send N / --bench-sink измеряют чистый
  goodput через транспорт, не задевая сеть хоста.

## Требования

1. **Go 1.26.3+** - для сборки бинарника десктопного клиента / выходной ноды.
2. **Android NDK r27+** - для сборки бинарника Android-клиента.
3. **Xcode 26.6+** - для сборки бинарника iOS-клиента.
4. **Linux VPS / VDS** для выходной ноды (или запуск exit локально через
   QEMU, см. ниже).

## Структура

OpenFlux/
  main.go                          # Точка входа CLI (клиент / exit-node / бенчи)
  bench.go                         # Хелперы бенчмарка (--bench-send/--bench-sink)
  tun_darwin.go                    # macOS utun L3-клиент
  tun_watch.go                     # Watcher сокетов для bypass-маршрутов
  tun_other.go                     # Заглушки для не-darwin платформ
  export_ios.go                    # cgo-мост для iOS-статической библиотеки
  transport/
    transport.go                   # Интерфейс Transport
    batched.go                     # BatchedTransport (склейка + zstd)
    framing.go                     # Wire-формат батчированных кадров
    compressor.go                  # Legacy per-packet LZ4-кодек
    encrypted.go                   # Опциональная AES-256-GCM обёртка
    yandex/                        # Бэкенды Yandex.Docs + Volga
    oneme/                         # Бэкенд MAX Messenger
    cupsonline/                    # Бэкенд Cups.online
  tunnel/
    tunnel.go                      # Клиентский туннель (gVisor + TunnelLinkEndpoint)
    endpoint.go                    # Виртуальный NIC (клиент)
    exit.go                        # Диспетчер NewExitNode (l3 / proxy)
    proxy_exit.go                  # Legacy proxy-exit (gVisor + net.Dial)
    l3/                            # L3-выходная нода
      l3.go                        # L3Exit: SNAT/DNAT, conntrack, фильтр egress
      backend.go                   # Интерфейс L3Backend
      backend_linux.go             # SOCK_RAW (Linux)
      backend_windows.go           # WinDivert (stub)
      backend_other.go             # Заглушка для неподдерживаемых платформ
      conntrack.go                 # Таблица conntrack
      flow.go                      # Flow-ключи, SNAT/DNAT, checksums
    rawsocket_linux.go             # Legacy raw exit (оставлен для референса)
    rawsocket_{darwin,windows}.go
  socks5/                          # SOCKS5-сервер (fallback на клиенте)
  network/                         # Контрольные суммы, разбор пакетов
  utils/                           # Логирование
  ios-app/                         # iOS-клиент на SwiftUI (XcodeGen)
  build_ios.sh                     # Сборка статической библиотеки iOS (liboflux.a)
  build_ios_app.sh                 # Сборка + архив + экспорт IPA iOS
  build_android.sh                 # Сборка клиентского бинарника Android
  scripts/
    cleanup-utun.sh                # Удалить stale-маршруты utun (macOS)
    build-flx-linux-img.sh         # Сборка минимального Alpine rootfs для QEMU

## Сборка

go mod tidy
go build -o openflux .

Кросс-сборка для выходной ноды (Linux amd64), stripped:

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags=\"-s -w\" -trimpath -o openflux-linux .

## Использование

### Выходная нода (Linux, L3-режим)

L3-режим форвардит сырые IPv4-пакеты между транспортом и сетевым стеком ОС.
Требует root (CAP_NET_RAW).

sudo ./openflux --exit-node --mode l3 \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Добавьте --debug для подробного лога. На Linux правило iptables не
требуется - L3-код сам дропает исходящие RST перед sendto().

### Выходная нода (legacy proxy-режим, без root)

./openflux --exit-node --mode proxy \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Proxy-режим - fallback для платформ, где L3 недоступен (Windows без
WinDivert, macOS или Linux без root).

### Клиент - macOS L3 (utun)

sudo ./openflux --client --tun \
    --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\"

Создаёт utun-интерфейс, ставит bypass-маршруты для транспорта, ждёт
подключения транспорта, затем забирает default-маршрут. SOCKS5 не нужен.

Требует sudo. Весь трафик, кроме транспорта, идёт через туннель.

### Клиент - SOCKS5 (все платформы, fallback)

./openflux --client --transport yandex \
    --url \"YOUR_YANDEX_DOC_URL\" \
    --socks5 :1080

Настройте браузер на 127.0.0.1:1080 как SOCKS5-прокси.

### Выбор кодека

По умолчанию транспорт использует батчированный + zstd кодек
(transport/batched.go + transport/framing.go). Для старого
per-packet LZ4-кодека передайте --legacy:

./openflux --client --legacy ...   # на обеих сторонах

Важно: батчированный wire-формат НЕ совместим с legacy LZ4.
Клиент и выходная нода должны использовать один и тот же кодек
(оба - новые, либо оба - --legacy).

### Бенчмарки

Измерьте чистый goodput через транспорт, не задевая сеть хоста:

# Отправитель: залить 100 MB
./openflux --client --transport yandex --url \"...\" --bench-send 100

# Приёмник: измерить goodput
./openflux --client --transport yandex --url \"...\" --bench-sink

### Другие транспорты

# Yandex Volga (HTTP relay)
./openflux --exit-node --mode l3 --transport vyandex --url \"...\" --debug

# MAX / OneMe (WebRTC DataChannel)
./openflux --exit-node --mode l3 --transport oneme \
    --maxToken \"...\" --maxUid \"...\" --debug

# Cups.online (Centrifugo-комнаты)
./openflux --exit-node --mode l3 --transport cupsonline --debug
# печатает base64-список комнат; передайте его клиенту через --url

## TODO

- **Запуск выходной ноды без VPS (QEMU).** Минимальный образ Alpine Linux
  (~13 MB) может хостить выходную ноду на любом десктопе (macOS / Windows /
  Linux) с установленным QEMU. Базовые файлы (vmlinuz-virt +
  base-initramfs.gz) собираются один раз; пользовательский образ
  пересобирается за ~3 секунды с бинарём oflx и URL транспорта.
  Пока не поставлено — трекается как будущее дополнение.

- **Windows L3-клиент.** L3-выход работает на Linux (SOCK_RAW) и заглушен
  для Windows (WinDivert). Подключить WinDivert-бэкенд к L3-форвардеру —
  запланировано.

- **Дополнительные транспорты.** Новые бэкенды можно реализовать против
  интерфейса Transport; батчированный кодек оборачивает любой из них.

- **Публичное распространение в App Store.** Текущий iOS-билд — только
  TestFlight-internal (Guideline 5.4 требует NetworkExtension-таргет и
  organization-аккаунт для публичных VPN-приложений).


## Флаги

| Флаг | По умолчанию | Описание |
|------|--------------|----------|
| --client | | Запуск в режиме клиента |
| --exit-node | | Запуск в режиме выходной ноды |
| --tun | false | macOS-клиент: utun L3-режим (нужен sudo) |
| --socks5 | :1080 | Адрес SOCKS5-прокси |
| --url | https://localhost | URL документа (Yandex Docs, Cups base64-список) |
| --transport | yandex | yandex, vyandex, oneme, cupsonline |
| --mode | l3 | Режим выходной ноды: l3 (raw forward) или proxy (gVisor + net.Dial) |
| --legacy | false | Использовать legacy per-packet LZ4-кодек вместо батчинга |
| --encryption-key-file | | Опциональная AES-256-GCM обёртка (общий секрет) |
| --maxToken | | Токен авторизации (MAX) |
| --maxUid | | ID пользователя (MAX) |
| --bench-send | 0 | Бенчмарк: залить N MB и выйти |
| --bench-sink | false | Бенчмарк: принять и измерить goodput |
| --bench-compressible | false | Бенчмарк: использовать сжимаемый payload |
| --debug | false | Включить подробное логирование |

## Реализация собственных транспортов

Реализуйте интерфейс Transport из transport/transport.go и
зарегистрируйте свой транспорт в switch-блоке main.go. Батчированный кодек
(BatchedTransport) оборачивает любой транспорт - новый бэкенд получает
батчинг бесплатно.

## Лицензия

Проект распространяется под лицензией GNU General Public License v3.0 or
later. Полный текст - в файле LICENSE.

Лицензии третьих сторон - в файле NOTICE.

## Дисклеймер

Только для образовательного использования. Тестируйте на собственных
машинах и сетях.


# Go Distributed File Storage

یک File Storage توزیع‌شده آموزشی با Go و PostgreSQL. Coordinator فایل را با حافظه محدود به Chunkهای پیوسته تقسیم می‌کند و هر Chunk را روی یک Storage Node مستقل می‌فرستد. PostgreSQL فقط Metadata را نگه می‌دارد و بایت‌ها روی Volume هر Node ذخیره می‌شوند.

## محدودیت‌های نسخه 1.0

- برای Upload حداقل دو Storage Node سالم لازم است. تعداد Nodeها هنگام شروع Upload ثابت می‌شود و Placement تا پایان همان Upload تغییر نمی‌کند.
- هر فایل به تعداد Nodeهای انتخاب‌شده Chunk دارد. Replication و تحمل خرابی دائمی Node وجود ندارد؛ خرابی یک Node یعنی فایل قابل بازیابی نیست.
- اندازه فایل به‌صورت پیش‌فرض حداکثر 100 MiB و تعداد Nodeهای انتخابی حداکثر 32 است. هر Node باید ظرفیت کافی برای Chunk تخصیص‌یافته داشته باشد.
- Upload چندکاربره و احراز هویت کاربر پیاده‌سازی نشده است؛ فایل‌ها به یک مالک داخلی ثابت تعلق می‌گیرند. ارتباط داخلی Nodeها با توکن محافظت می‌شود.
- Download ابتدا کل فایل را در فایل موقت محلی بازسازی می‌کند و checksum هر Chunk و فایل کامل را کنترل می‌کند؛ Coordinator برای این کار به فضای موقت دیسک به‌اندازه فایل نیاز دارد.

## اجرای سریع با Docker Compose

Docker و Docker Compose را نصب کن، سپس در ریشه پروژه اجرا کن:

```sh
docker compose up --build
```

Compose یک PostgreSQL مستقل برای این Stack روی پورت `5433`، سه Storage Node روی پورت‌های `8081` تا `8083` و Coordinator روی `8080` بالا می‌آورد. Migration اولیه پیش از Coordinator اجرا می‌شود. برای توکن داخلی، مقدار `INTERNAL_TOKEN` را در فایل `.env` تنظیم کن؛ مقدار پیش‌فرض فقط برای اجرای محلی است.

برای بررسی سلامت Coordinator و Nodeها:

```sh
curl http://localhost:8081/internal/v1/health
curl http://localhost:8082/internal/v1/health
curl http://localhost:8083/internal/v1/health
```

Compose داده PostgreSQL و Nodeها را در Volumeهای مستقل نگه می‌دارد. `docker compose down` کانتینرها را متوقف می‌کند و Volumeها را نگه می‌دارد.

## استفاده از PostgreSQL نصب‌شده روی سیستم

اگر PostgreSQL خودت روی `localhost:5432` اجراست، نام DB=`gostorage_db`، کاربر=`gostorage` و رمز=`gostorage` را در DSN قرار بده. این دستور فقط Migration پروژه را روی همان DB اعمال می‌کند:

```sh
export DATABASE_URL='postgres://gostorage:gostorage@localhost:5432/gostorage_db?sslmode=disable'
go run ./cmd/migrate up
```

سپس Coordinator را اجرا کن:

```sh
export INTERNAL_TOKEN='replace-with-a-long-random-secret'
export LISTEN_ADDR=':8080'
go run ./cmd/coordinator
```

در سه Terminal دیگر، Nodeها را اجرا کن. هر Node باید ID پایدار و UUID، آدرس قابل دسترس از Coordinator، دایرکتوری مستقل و دقیقاً همان توکن داخلی داشته باشد؛ مقدار نمونه زیر را در هر چهار فرایند یکسان قرار بده:

```sh
LISTEN_ADDR=':8081' NODE_ID='10000000-0000-4000-8000-000000000001' NODE_NAME='node1' NODE_ADDR='http://127.0.0.1:8081' COORDINATOR_URL='http://127.0.0.1:8080' STORAGE_DIR='./data/node1' INTERNAL_TOKEN='replace-with-a-long-random-secret' go run ./cmd/storage-node
```

```sh
LISTEN_ADDR=':8082' NODE_ID='20000000-0000-4000-8000-000000000002' NODE_NAME='node2' NODE_ADDR='http://127.0.0.1:8082' COORDINATOR_URL='http://127.0.0.1:8080' STORAGE_DIR='./data/node2' INTERNAL_TOKEN='replace-with-a-long-random-secret' go run ./cmd/storage-node
```

```sh
LISTEN_ADDR=':8083' NODE_ID='30000000-0000-4000-8000-000000000003' NODE_NAME='node3' NODE_ADDR='http://127.0.0.1:8083' COORDINATOR_URL='http://127.0.0.1:8080' STORAGE_DIR='./data/node3' INTERNAL_TOKEN='replace-with-a-long-random-secret' go run ./cmd/storage-node
```

Nodeها هر 10 ثانیه heartbeat می‌فرستند. Coordinator فقط Nodeهای `active` با heartbeat تازه و ظرفیت کافی برای Placement انتخاب می‌کند. برای تولید، از توکن تصادفی قوی و شبکه داخلی محافظت‌شده استفاده کن.

## API نمونه

Upload با `multipart/form-data`:

```sh
curl -f -F 'file=@./example.bin' http://localhost:8080/api/v1/files
```

پاسخ شامل `id`, `size_bytes`, تعداد Chunkها، SHA-256 و status است. `id` را برای درخواست‌های بعدی نگه دار:

```sh
curl -f 'http://localhost:8080/api/v1/files?limit=50'
```

برای نمایش خلاصه فایل‌ها در صورت نصب `jq`:

```sh
curl -fsS 'http://localhost:8080/api/v1/files?limit=50' | jq -r '.files[] | [.id, .name, .size_bytes, .sha256] | @tsv'
```

دانلود و بررسی تطبیق فایل:

```sh
curl -f http://localhost:8080/api/v1/files/<file-id>
curl -f -o ./downloaded.bin http://localhost:8080/api/v1/files/<file-id>/content
sha256sum ./example.bin ./downloaded.bin
cmp ./example.bin ./downloaded.bin
curl -f -X DELETE http://localhost:8080/api/v1/files/<file-id>
```

Delete تکراری بی‌خطر است. فایل فقط پس از ذخیره و تأیید تمام Chunkها `active` می‌شود؛ Upload ناقص `failed` می‌ماند. فایل‌های نیمه‌تمام منقضی‌شده به‌صورت دوره‌ای پاک‌سازی می‌شوند.

## Migration

Migrationها در `db/migrations/` نگهداری می‌شوند و با نسخه در جدول `schema_migrations` ثبت می‌شوند. اجرا و Rollback:

```sh
go run ./cmd/migrate up
go run ./cmd/migrate down
```

`down` جداول نسخه 1.0 را حذف می‌کند؛ پیش از استفاده روی محیط دارای داده، از DB پشتیبان بگیر.

## تست و ابزار توسعه

```sh
make fmt
make test
make vet
make race
make build
```

تست‌های PostgreSQL و End-to-End فقط وقتی اجرا می‌شوند که `TEST_DATABASE_URL` تنظیم شده باشد. آن‌ها به DB مهاجرت‌شده وصل می‌شوند، دو Storage Node محلی موقت بالا می‌آورند و رکوردهای ساخته‌شده را پاک می‌کنند. برای این تست‌ها بهتر است DB اختصاصی تست استفاده شود:

```sh
TEST_DATABASE_URL='postgres://gostorage:gostorage@localhost:5432/gostorage_db?sslmode=disable' go test ./tests/integration
```

تست‌ها Chunk Planner، انتخاب Node سالم، checksum، Storage Node، Upload/Download round-trip، حذف تکراری، خرابی Node و Chunk خراب را پوشش می‌دهند.

## شناخته‌شده و خارج از Scope

این نسخه Replication، Erasure Coding، Resume، S3 API، رمزنگاری فایل، Rebalancing، نسخه‌بندی، سهمیه و احراز هویت چندکاربره ندارد. خطای دائمی یا از دست رفتن هر Node می‌تواند دسترسی به فایل‌های دارای Chunk روی آن Node را از بین ببرد.

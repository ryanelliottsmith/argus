# GeoIP test fixtures

These small, synthetic MaxMind test databases come from `maxmind/MaxMind-DB`
revision `d1551916cff0712d12ff602c309234471b0402a2`, under `test-data/`:

- `GeoLite2-ASN-Test.mmdb`, stored here as `GeoLite2-ASN.mmdb`.
- `GeoIP2-City-Test.mmdb`, stored here as `GeoLite2-City.mmdb` to match the
  application's configured file names.

They are test fixtures only, not production GeoLite databases. Tests copy them
into temporary directories, so they need no MaxMind account, downloads or
Internet access.

Copyright (c) 2013 - 2026 MaxMind, Inc. Distributed under the MIT license included
in `LICENSE-MIT` (upstream also offers Apache-2.0).

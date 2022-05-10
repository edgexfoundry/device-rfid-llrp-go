# Device RFID LLRP

## Change Logs for EdgeX Dependencies

- [device-sdk-go](https://github.com/edgexfoundry/device-sdk-go/blob/main/CHANGELOG.md)

## [v2.2.0] - Kamakura - 2022-05-11  (Not Compatible with 1.x releases)

### Features ✨

- Update to latest go-mod-messaging w/o ZMQ on windows ([#a222f54](https://github.com/edgexfoundry/device-sdk-go/commits/a222f54))

  ```
  BREAKING CHANGE:
  ZeroMQ no longer supported on native Windows for EdgeX
  MessageBus
  ```

## [v2.1.0] Jakarta - 2022-04-27  (Not Compatible with 1.x releases)
### Features ✨
- Migrate service to V2 ([#52](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/52)) ([#60419ad](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/60419ad))
- added CORS configuration to service section in configuration TOML ([#67](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/67)) ([#e0db465](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/e0db465))
### Test
- **snap:** add snap CI workflow ([#73](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/73)) ([#8b44d22](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/8b44d22))
### Bug Fixes 🐛
- updated inventory_service link from https://github.com/edgexfoundry-holding/rfid-llrp-inventory-service to https://github.com/edgexfoundry/app-rfid-llrp-inventory ([#76](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/76)) ([#8fb081e](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/8fb081e))
- misuse of unbuffered os.Signal channel error ([#56](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/56)) ([#2631263](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/2631263))
### Build 👷
- update alpine base to 3.14 ([#49](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/49)) ([#e0ebc7e](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/e0ebc7e))
- Added "make lint" to target and added it to make test. Resolved all lint errors as well ([#60](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/60)) ([#fdb8dc9](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/fdb8dc9))

<a name="v1.0.0"></a>
## [v1.0.0] - 2021-08-20 (Only Compatible with 1.x releases)
### Feature
- **discover:** trigger debounced discovery on consul config change ([#35](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/35)) ([#8772bf5](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/8772bf5))
### Bug Fixes 🐛
- update all TOML to use quote and not single-quote ([#46](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/46)) ([#8818edd](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/8818edd))
- Disable UpdateLastConnected in toml file ([#45](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/45)) ([#2489b51](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/2489b51))
- use provision watcher logic for discovered devices ([#42](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/42)) ([#a792bd6](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/a792bd6))
- invalid boolean logic in UpdateDevice code ([#41](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/41)) ([#b1162a6](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/b1162a6))
- **config:** Add CheckInterval to toml file for registry ([#37](https://github.com/edgexfoundry/device-rfid-llrp-go/issues/37)) ([#13be571](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/13be571))
- **url:** remove holding suffix from module url ([#b528ca6](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/b528ca6))
### Documentation 📖
- Add badges to readme ([#84d0a8d](https://github.com/edgexfoundry/device-rfid-llrp-go/commits/84d0a8d))

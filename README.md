# Chrome Offline Mirror

自动镜像 Chrome 全平台离线安装包到 GitHub Release，每日更新。

[![Daily Chrome Download](https://github.com/LOVECHEN/chrome-release/actions/workflows/daily-download.yml/badge.svg)](https://github.com/LOVECHEN/chrome-release/actions/workflows/daily-download.yml)

## 下载

前往 [Releases](https://github.com/LOVECHEN/chrome-release/releases) 页面下载对应平台的安装包。

### 支持平台

| 平台 | 文件格式 |
|------|---------|
| Windows 64-bit | `.msi` |
| Windows 32-bit | `.msi` |
| macOS (Universal) | `.dmg` |
| Linux (Debian/Ubuntu) | `.deb` |
| Linux (Fedora/RHEL) | `.rpm` |

### 支持渠道

| 渠道 | 说明 |
|------|------|
| Stable | 正式版，推荐日常使用 |
| Beta | 测试版，提前体验新功能 |
| Dev | 开发版，最新但可能不稳定 |

## Release 命名规则

```
{channel}-{version}
```

示例：
- `stable-146.0.7680.76` — Stable 正式版
- `beta-147.0.7727.3` — Beta 测试版
- `dev-148.0.7730.2` — Dev 开发版

## 自动化

GitHub Actions 每日 UTC 06:00（北京时间 14:00）自动执行：

1. 查询 [Google VersionHistory API](https://versionhistory.googleapis.com/v1/chrome/platforms/win64/channels/stable/versions/all/releases) 获取最新版本号
2. 对比已有 Release，版本未变则跳过
3. 下载全平台离线安装包
4. 生成 SHA256 校验文件
5. 创建 GitHub Release 并上传

## 本地使用

本项目还包含一个 Go CLI 工具，可在本地手动下载：

```bash
# 编译
go build -o chrome-downloader .

# 查看版本信息
./chrome-downloader -info

# 下载 macOS stable
./chrome-downloader -channel stable mac

# 下载全平台全渠道
./chrome-downloader all
```

## 数据源

- 版本信息：[Google VersionHistory API](https://versionhistory.googleapis.com)
- 安装包：[dl.google.com](https://dl.google.com) 官方 CDN

## License

MIT

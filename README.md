# Cadence Forge · 音视频成片服务

Go HTTP 后端，以真实音视频字节为输入，提供版本化剪辑、字幕时间映射、MP4/WAV加工和可核验交付。没有Web前端。

## 运行

需要 Go 1.24+、Python 3（仅运行有界工作流检查）及带 libx264、AAC、loudnorm、tile、showwavespic 的 FFmpeg/ffprobe。当前本机实际验证 Go 1.27、FFmpeg/ffprobe 9.0.2；Docker构建定义采用Go 1.24和Debian媒体包，镜像运行尚需目标环境独立验证。

```sh
go build -trimpath -o bin/forged ./cmd/forged
./bin/forged
```

默认监听 `127.0.0.1:8734`，数据根 `var/cadence-forge`。可以设置 `FORGE_ADDR`、`FORGE_ROOT`、`FORGE_WORKERS`（1..8，默认2）、`FFMPEG_BIN`、`FFPROBE_BIN`。同一数据根仅允许一个服务进程。服务作为受信任环境中的媒体后端运行，面向公网接入前应部署认证和网络边界。

使用现有媒体工具运行所有真实工作流：

```sh
python3 scripts/workflow_check.py --workflow all
```

检查在临时数据根内生成4秒校准音画及一份三路测试片，不依赖外部素材或在线服务，完成19条工作流，包括真实强制终止及恢复。`--workflow`可选择运行清单中的某条工作流；所需的前置接收、版本编辑和渲染也会执行。初始化基线不含自动测试套件，testing为deferred；这里提供有界生产工作流核验，不声明测试命令。

## 调用顺序

1. `POST /forge/v1/sources?name=校准片`：请求体为媒体原始字节，返回素材ID、SHA-256与流事实。
2. `POST /forge/v1/recipes`：例如 `{"name":"审片","format":"mp4","width":640,"height":360,"fps":24,"sample_rate":48000,"normalize":false,"target_lufs":-16}`。
3. `POST /forge/v1/sheets`：`{"name":"剪辑","spans":[{"source_id":"素材ID","in_ms":0,"out_ms":1500,"gain_db":0,"audio_takes":[{"stream":0,"role":"program"}]}],"cues":[{"source_id":"素材ID","start_ms":500,"end_ms":1000,"text":"校准"}]}`。`audio_takes` 可省略，省略时等同单条 `{"stream":0,"role":"program"}`。`stream` 是探测结果中的音频流序号（与 FFmpeg 的 `a:N` 一致），`role` 取值 `program`、`field`、`reference`。
4. `GET /forge/v1/sheets/{id}/preview?recipe_id={id}`：预览当前最新修订的累计区间及WebVTT。片段按半开区间求交；同源重复出现分别映射。
5. `POST /forge/v1/renders`：提供 `Idempotency-Key`，正文 `{"sheet_id":"剪辑ID","sheet_revision":1,"recipe_id":"配方ID","recipe_revision":1}`。必须显式选择修订，提交冻结引用与时间计划；预览最新版本不替代提交选择。
6. `GET /forge/v1/renders/{id}`：查看排队、运行、成功、失败或取消及尝试历史。
7. `GET /forge/v1/renders/{id}/artifact`：下载检验通过的媒体；`?captions=1`下载字幕，`?stem=program|field|reference`下载该独立声音分轨。`POST .../verify`重新核验媒体与全部分轨；`GET .../bundle`返回媒体、字幕、分轨及冻结说明组成的ZIP。

## 声道路由

素材接收时记录每路音频流的序号、编码、声道数与布局。剪辑片段用 `audio_takes` 把这些序号分别路由到 `program`、`field`、`reference` 三条独立声音路径；同一片段内同一流或同一角色只能出现一次，序号必须落在已探测事实内。流身份在预览与提交时都解析自首次探测结果，不会在渲染时重新探测猜测。

提交时冻结一份路由决定，记录每条路径的流序号、来源、实测 RMS/峰值、逐声道相位关系，以及它进入双声道审片的方式。渲染只执行这份冻结记录：每条路径各自成链并保持与画面相同的累计时间，任何未参与的片段补等长静音。

两条交付同时产生：审片 MP4/WAV 是双声道混音；`stem-*.wav` 为每条路径的独立双声道分轨，保持作者设定的增益与原始极性。分轨与审片使用同一次冻结计划，不重复解码出不同时间基准。

三路及以上、且部分路径反相时，“每路都可单独处理”和“双声道审片听清关键内容”无法用固定叠加同时满足。服务不会把反相内容在分轨里改写，而是在审片中按实测相位纠正冲突路径、对非锚点路径降低 `-6 dB`，并把取舍写进失损说明（`loss_notes`）：哪些路径被排除、为什么、分轨保留了什么。策略由配方 `audio_review_policy` 选择，`audio_review_anchor` 指定优先路径：

- `intelligible`（缺省）：非锚点路径在审片中衰减 `-6 dB`，冲突极性按锚点纠正；分轨不变。
- `full-sum`：全部可听路径按作者增益叠加，仅做峰值限制；安静路径可能被掩蔽。
- `anchor-only`：审片只保留锚点路径，其余路径仅存在于分轨。

单路径剪辑沿用原有双声道输出，不产生分轨。测量窗口上限为累计时长与 30 秒中的较小值，16 kHz 采样，只作为路由依据，不等于主观审片通过。

## 编辑与恢复

`PUT /sheets/{id}`及`PUT /recipes/{id}`使用十进制修订号 `If-Match: 1`。本服务的该头为应用级修订约定，并非通用HTTP条件请求实现。保存只追加历史，过期写入409。输入中的服务管理ID与修订由服务生成。

`POST /renders/{id}/cancel`撤回排队或运行中的交付；成功确认后该任务不能新提交成片。进程停止是后续动作，并不承诺确认瞬间资源已全部释放。成功成片不可再取消。`POST /renders/{id}/retry`仅接受failed，最多三次尝试，沿用原冻结输入。重启时running变为failed/interrupted，等待显式重试，queued继续排队。

`GET /events?after=0`返回至多200条严格后继事件。序号是持久顺序，时间戳仅用于解释。`GET /state`返回当前一致快照，适合受信任客户端。

## 分析入口

`POST /sources/{id}/loudness`测量源LUFS、真峰值和动态范围，静音或不可测结果使用null并指示silent。`GET /sources/{id}/visual?mode=storyboard`生成四格图；`mode=waveform`生成640×120单声道概览。它们是有成本的同步分析，请求受独立超时约束；波形合并声道可能隐藏反相内容，故事板不等于逐帧审片。

## 支持范围与限制

单素材最多64 MiB、约120秒、3840×2160和8声道（最多8条音频流）。剪辑最多32段、120秒、500条字幕，段长至少100ms；输出MP4/H.264/AAC或WAV/16位PCM，采样率44.1或48kHz。MP4要求每段有视频；无声视频生成对应静音。WAV要求每段有声音。视频保持完整画面并留边，统一像素比例与帧率，声音统一立体声。归一化为单遍响度滤镜，不保证任何短片都精确达到目标值。分轨始终为16位双声道PCM，与主交付同采样率、同累计时长。

时长基准为毫秒，编码后的帧与采样可能有容差。检验检查流、尺寸、采样率、时长与摘要，不代表内容正确、逐帧同步或主观质量已通过人工审片；分轨检验只证明其为符合契约的双声道PCM，不证明其内容就是期望的那路声音。禁止容器中的远程协议与非白名单封装，但媒体进程仍需要部署侧隔离。

接收先耐久保留字节再探测登记，失败可能留下未引用对象。成功产物先检验、耐久提升，再提交可见事实；崩溃可能留下未登记产物。当前不自动回收这些内容。`DELETE /sources/{id}`只退休无历史引用的素材记录，保守保留对象；历史剪辑、字幕和渲染血缘均阻止退休。

状态写入使用同文件系统临时文件、fsync、替换与父目录同步；目录同步失败时服务拒绝后续写入并要求恢复核对。对象及产物与状态分阶段提交，已生成目录不能自行视为交付成功。请备份整个数据根，并在停止唯一服务实例后进行一致复制。

## 容器

```sh
docker build -t cadence-forge .
docker run --rm -p 8734:8734 -v cadence-forge-data:/data cadence-forge
```

运行身份为10001。这里的容器定义不是跨平台运行通过声明；实际验证范围与本机工作流结果见交付记录。

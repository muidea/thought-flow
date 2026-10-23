# ThoughtFlow 专题文档工作区收口设计

版本：v1.0\
日期：2026-10-23\
代码基线：`7493027803e91230e1ca0d460e0294e13a07d1e0`，`master`\
文档定位：面向实现、评审和验收的设计基线。文中新增模型、接口、目录和行为均为待实现设计，不表示当前代码已经提供。本文不授权自动迁移现有用户文件。

## 本轮范围修订（2026-10-23）

用户已明确说明“当前阶段不用考虑历史数据兼容性”。本轮不要求旧布局数据自动升级、旧 runtime 私有证据迁移/回退、旧 profile 或旧客户端参数兼容；此前涉及这些兼容性的要求由本节取代。现有 aggregate 功能、新布局内显式模式转换、不可变修订的历史读取、当前操作的崩溃恢复和数据保护仍在范围内，不能以此豁免。

验收 T01、T32 的历史升级部分为正式豁免；T25 的普通 note 状态和 T42 的显式 scope 校验仍须验证。实现记录必须逐项区分自动化、代码核对、未验证与正式豁免，不能据本范围修订宣称全部验收通过。

## 1 结论与目标

在现有 Topic 上增加 `collection` 文档集模式，将多个独立 thought 组织为有目录、有顺序、可追踪的专题；将专题主归属文档及其业务文件集中存储在同一物理父目录；同时允许控制每篇 thought 是否参与全局检索。

核心职责分工如下：

- **Topic**：专题身份、默认策略、组织和阅读入口。
- **Catalog**：分组、阅读顺序、计划条目及成员关联。
- **Thought**：独立内容对象与工作记录。
- **Document Revision**：某篇正式文档的不可变正文、contract、profile 和来源快照。
- **Object Placement**：唯一物理归属及实际文件位置。
- **Search Policy**：全局检索参与策略，独立于物理位置和成员关系。
- **Topic Release**：可选的后续专题基线，固定成员与文档版本，不替代单文档定稿。

专题不是把所有成员重新生成成一篇文章。目录必须由结构化事实确定性生成；模型只能辅助建议，不能作为目录、状态或文件归属的权威来源。

### 1.1 已确认的用户要求

| 编号 | 要求 | 本文对应章节 |
|---|---|---|
| U1 | 多篇 thought 按软件或主题形成专题，支持模块设计组成完整文档集 | 4、6、10、14 |
| U2 | 提供专题索引，统一查看和跟踪成员文档 | 6、10、14 |
| U3 | 专题涉及的文件归拢到统一物理父目录管理和存放 | 5、7、8、9、15 |
| U4 | 允许专题下 thought 选择是否参与全局检索 | 11、12、14 |
| U5 | 明确便捷的专题查看方式，使用户能快速定位、连续阅读与跟踪 | 14、19 |

以下是为消除实现歧义而确定的**本方案设计决策**：唯一主归属、多专题引用、专题默认策略与 thought 覆盖、保留旧聚合模式、迁移显式执行、私有证据与可分享内容隔离。这些决策是本文推荐的实现基线，不应倒推为用户已单独确认过每项细节。

### 1.2 第一版必须完成的范围

第一版必须同时交付专题编排、物理集中存储、检索范围控制，不能只完成目录 UI 后宣称全部收口。

第一版包括：

1. `collection` 模式及与原模式的兼容。
2. 一级分组、排序、必需/可选条目、计划占位、手工加入/移除。
3. 独立文档查看与可信状态展示。
4. 专题物理根目录、新建即归属、已有内容显式迁移与故障恢复。
5. 资源清单、附件安全读取、历史链接解析与私有证据保护。
6. 专题默认和单 thought 检索覆盖，显式全局/专题范围。
7. 全局路由、搜索和索引重建、Git 白名单、迁移测试。
8. Markdown 索引导出、基础交付包与完整备份的明确区分。

第二阶段扩展：跨文档依赖复核、专题级一致性评审、专题发布基线及基线间差异。

### 1.3 非目标

- 不增加多用户权限系统，不把检索排除当作访问控制。
- 不实现多进程或分布式多写者；单工作区只允许一个服务写者。
- 不把文档 `ready_for_use` 等同软件开发完成、测试通过或上线完成。
- 不自动合并所有 thought 正文，不自动重排人工确认目录。
- 不依赖符号链接实现专题自包含，不创建多个同 ID 可独立编辑的原件。
- 不在迁移时改写历史 revision、自动升级历史 profile，或重新调用模型生成正文。
- 不因专题存储调整而新增外部模型调用、远端 Git 同步或公开分享授权。
- 第一版不实现任意层级专题树、复杂项目管理或全文语义一致性证明。

## 2 当前实现基线

本节是源码核对结论；后续章节是目标设计。

| 当前能力 | 当前事实 | 对本次设计的影响 |
|---|---|---|
| Topic 身份与成员 | `Topic` 含规则、Members、Outline、AutoWeave；Outline 只有标题 | 不能表示分组归属、排序和计划条目 |
| 成员状态 | `TopicMembership.Status` 表示 accepted/woven 等归入与编织状态 | 不可复用为设计完成或文档定稿状态 |
| Topic 正文 | `topics/<slug>/index.md` 是可编织、可保留人工内容的专题正文 | 新目录不可覆盖旧文件 |
| 自动编织 | `AddMembership` 会编织正文；关闭 AutoWeave 后 refresh 不接纳新的自动匹配成员 | collection 需要独立操作语义，不能只关闭一个开关 |
| Thought 路径 | 默认由 ID 日期推导 `thoughts/<年>/<月>/<id>.md` | 移动文件必须改造解析层和扫描入口 |
| 文档版本 | 全局 `documents/<id>/` 下已有不可变 revisions、HEAD、reports、finalizations | 复用版本模型，改变位置解析而非重建版本语义 |
| 私有来源证据 | 位于运行时 `document-evidence/<id>/`，不是普通正文 | 集中存储不能导致 Git 或导出泄漏 |
| 附件 | 工作区已有 attachments 路径；未据此证明完整附件管理和迁移已实现 | 本次需补资源登记与迁移能力，不能假定现成 |
| 搜索 | SearchQuery 含 TopicID；共享过滤使用精确成员匹配 | 增加检索范围和参与策略，不能仅前端隐藏 |
| HTTP 搜索 | 当前公开搜索入口以关键词流程为主；内部存在语义/混合路径 | 本版不顺带承诺新的公开搜索模式，但所有已有内部召回必须应用策略 |
| 文档交付 | 正式交付正文来自冻结 revision；导出不重新生成 | 专题查看和导出必须标明具体版本 |
| UI | 现有成员卡片提供预览和加入创作来源 | 新增编排、计划项、版本与状态视图 |

源码依据见第 21 节。此前在 `5891727` 完成的审计收口结论，不等于已对 `7493027` 做过重新全量审计。本设计编制只核对相关实现；没有重新执行 Go、浏览器、真实模型或迁移测试。

## 3 术语与不变量

### 3.1 三个相互独立的关系

1. **物理归属**：一个 thought/document 对象只有一个权威位置，`owner_topic_id` 可为空。
2. **专题关联**：同一对象可被多个专题目录引用；改变关联不自动改变归属。
3. **检索范围**：决定对象是否进入全局检索结果；不决定是否存在索引、不决定链接访问权限。

### 3.2 必须保持的不变量

| 编号 | 不变量 |
|---|---|
| I1 | 同一工作区同一 object ID 在任意时刻只有一个已提交权威位置 |
| I2 | 目录条目引用稳定 ID，不将绝对文件路径作为对象身份 |
| I3 | 计划条目不是已存在 thought，不伪造对象 ID |
| I4 | 已定稿与有新版草稿可以同时成立，不用单一状态覆盖 |
| I5 | 迁移不改变正文、contract、profile、来源清单及历史 revision 的已有内容哈希 |
| I6 | `catalog.md`、反链、搜索索引和统计是派生数据，不能反写成权威成员事实 |
| I7 | 私有证据不进入普通 Git 提交、搜索摘要或默认交付包 |
| I8 | 全局排除策略在所有已支持的召回支路和结果补充路径上生效 |
| I9 | 移除目录条目不删除原 thought；删除原 thought不静默删除 collection 的计划/缺失条目 |
| I10 | 自动匹配只提出候选，不改变 collection 的正式目录和文件归属 |
| I11 | 目录编辑、检索策略编辑与迁移具备版本保护及可重放的操作身份 |
| I12 | 受支持的启动与降级流程阻止不兼容程序写入新布局；不得声称任意历史二进制自带此能力 |

## 4 Topic 模式与兼容策略

`Topic.mode` 取值：

- `aggregate`：现有聚合/编织行为。当前新建对象显式设置 mode；无 mode 的旧数据升级不在本轮范围。
- `collection`：文档集行为。显式目录管理，不自动 weave 成员正文。

collection 仍可保留 `index.md` 作为人工综述，或引用一篇“总体架构设计”thought。综述与成员目录是两个不同对象。

新建专题页面默认保留既有模式选择语义；用户选“文档集专题”才创建 collection。不能通过升级静默把旧专题转换成 collection。

模式转换要求显式预览：

1. 原正文和旧规则保留。
2. 旧成员按稳定顺序导入默认分组。
3. 原 accepted/woven 状态保留为历史，不映射为文档交付状态。
4. 转换只改变组织方式，不自动迁移文件。
5. 已存在主归属文件的 collection 不允许直接回退为 aggregate；必须先按迁移流程迁出或显式保留存储承载能力。

## 5 物理目录布局

### 5.1 专题工作区

```text
<workspace>/
  .content-registry/
    schema.json
    objects/<object-id>.json
  thoughts/<yyyy>/<mm>/<thought-id>.md       # 尚未归属专题的旧布局
  documents/<document-id>/                  # 对应未归属对象的旧布局
  document-formats/published/...            # 全局模板定义
  topics/<topic-id>/
    topic.yaml
    catalog.json
    catalog.md
    index.md
    thoughts/<thought-id>.md
    documents/<document-id>/
      head.json
      revisions/<revision-id>/
        body.md
        contract.json
        source-manifest.json
        profile.snapshot.md
        manifest.json
        projection.json
      reports/<report-id>.json
      finalizations/<finalization-id>.json
    attachments/<resource-id>/<filename>
    resources.json
    references/<snapshot-id>/
      snapshot.json
      body.md
      assets/...
    releases/<release-id>/                  # 第二阶段版本基线
    storage.snapshot.json                   # 位置清单的可重建投影
    .private/
      evidence/<document-id>/<hash>.txt
      migration-staging/<operation-id>/...
```

`topic-id` 使用创建后不变的安全目录键；现有 ID/Slug 可直接沿用，不因为显示名称修改而重命名目录。分组和顺序存在 catalog 中，不随拖动操作搬文件。

### 5.2 “全部归拢”的精确定义

- **主归属对象**的 thought 文件、文档版本、校验和定稿记录、已登记本地资源集中在专题根目录。
- **跨专题引用对象**保留唯一原件；如果本专题要求材料自包含，必须显式保存固定版本的只读快照及必要资源。
- **全局模板定义**仍统一维护；每个正式 revision 已包含使用版本的 profile 快照，不需要复制一个可变模板库。
- **全局位置登记**属于工作区路由元数据，不是另一份文档内容；它可以位于专题外。
- 服务日志、任务队列、锁、搜索数据库、临时模型请求及密钥不是专题业务交付文件，不纳入专题可分享目录。
- 私有证据可在专题根目录的 `.private` 中完整保存，但其访问、备份和分享策略独立。

“不下载外部 URL”与“资料自包含”不能同时声称成立。快照必须记录 `complete`、`partial` 或 `unavailable`，未获得资源时明确缺失，不伪造离线完整性。

## 6 权威数据模型

### 6.1 Topic 配置

```json
{
  "schema_version": 2,
  "id": "aetherrelay-design",
  "mode": "collection",
  "name": "AetherRelay 软件设计",
  "description": "整体架构与功能模块设计",
  "generation": 7,
  "search_policy": {
    "topic_global_visible": true,
    "owned_thoughts_global_default": false
  }
}
```

- `topic_global_visible`：专题入口是否可被全局搜索发现。
- `owned_thoughts_global_default`：本专题主归属 thought 的默认全局参与值。
- 新建专题的缺省值为 `true`，界面允许明确关闭。无登记数据重建保持保守排除；旧数据升级默认值已豁免。
- 两个开关独立。专题可见不代表成员逐篇可见。
- `generation` 由服务管理，每次有效配置变更递增。

### 6.2 Catalog

```json
{
  "schema_version": 1,
  "topic_id": "aetherrelay-design",
  "generation": 12,
  "required_delivery_level": "ready_for_use",
  "groups": [
    {"id": "g-overview", "title": "总体设计"},
    {"id": "g-core", "title": "核心功能"}
  ],
  "entries": [
    {
      "id": "e-architecture",
      "group_id": "g-overview",
      "title": "总体架构",
      "role": "design",
      "required": true,
      "binding": {
        "kind": "thought",
        "thought_id": "20261023-architecture",
        "version_policy": "latest_finalized"
      }
    },
    {
      "id": "e-routing",
      "group_id": "g-core",
      "title": "请求路由设计",
      "role": "design",
      "required": true,
      "binding": {"kind": "planned"}
    }
  ]
}
```

定义：

- 数组顺序就是展示顺序，不另设冲突的数字 rank。
- 第一版只有一级分组，至少存在一个默认分组；删除含条目的分组必须同时指定移动目标，否则拒绝。
- `entry.id` 在修改标题、绑定和排序后保持稳定。
- `binding.kind`：`planned`、`thought`、`snapshot`。
- `thought` 绑定支持 `version_policy=latest_finalized|pinned_finalized`；后者必须给出 `revision_id` 与 `finalization_id`，服务验证其确实已定稿。
- 没有定稿的 thought 仍可加入；索引显示“尚无定稿”，允许显式查看草稿，不能隐式降级为“最新正文就是定稿”。
- 普通 note 没有正式文档交付链时显示“普通笔记”，可作为辅助资料；必需设计条目不能以此计入已交付。
- `required_delivery_level` 默认为 `ready_for_use`；用户可将专题整体门槛明确改为 `proposal`。统计必须展示当前门槛。
- 同一专题不允许同一 thought 出现多个正式条目。跨分组重复导航以后作为别名扩展，第一版拒绝重复，避免重复统计。
- 跨专题引用不改变 owner。
- 条目的标题是专题内展示标题；原文档标题另行显示，不改写原件。

### 6.3 权威边界

- aggregate 模式继续以现有 Topic.Members 和 TopicMembership 维护原成员语义。
- collection 模式以 catalog 的条目绑定维护成员关系与顺序；旧 API 所需 Topic.Members、TopicIDs 反链和 MemberCount 均为派生兼容投影，不允许独立写入改变 collection 事实。
- owner 仅由对象位置记录维护；catalog 引用不能反向改变 owner。一个 owner 为 A 的对象可以不在 A 的目录中，也可以被 B 引用。
- topic.yaml 的策略与名称、catalog 的编排、位置记录的 owner/path/覆盖值分别只有一个权威写入口。
- 客户端改动派生字段必须被拒绝或忽略并明确返回说明，不能生成第二套成员或归属事实。

### 6.4 对象位置与检索覆盖

位置记录是每个对象的权威归属和检索覆盖记录：

```json
{
  "schema_version": 1,
  "object_id": "20261023-architecture",
  "object_kind": "thought",
  "generation": 4,
  "owner_topic_id": "aetherrelay-design",
  "global_search": "inherit",
  "placement": {
    "thought_path": "topics/aetherrelay-design/thoughts/20261023-architecture.md",
    "document_root": "topics/aetherrelay-design/documents/20261023-architecture",
    "evidence_root": "topics/aetherrelay-design/.private/evidence/20261023-architecture"
  },
  "last_operation_id": "op-move-architecture"
}
```

- `global_search=inherit|include|exclude`。默认 `inherit`。
- 不把 TopicIDs 的第一个元素当作 owner。owner 可以为空。
- `placement` 只接受服务器推导出的工作区相对路径，不接受任意客户端路径。
- 位置记录以 ID 命名，其自身不随文档移动。
- Thought front matter 可投影 owner 和覆盖策略用于可读性，但不是第二个可随意编辑的权威源；外部修改必须经校验导入流程，不在读取时静默覆盖登记。
- `document_root` 对无正式文档链的 note 可为空；不能据文件夹缺失自动创造文档交付状态。

### 6.5 资源模型

`resources.json` 保存资源 ID、相对路径、文件大小、MIME、内容哈希、可分享性、所属对象及引用关系。它是专题资源事实的权威清单，文件扩展名不构成可信类型或权限依据。

资源所有权独立于“哪个段落使用它”。同专题多个文档可引用一个资源；跨专题转移时必须计算引用集合，不可从源专题直接搬走仍被其他文档依赖的资源。必要时复制内容并保留稳定来源和哈希，但不复制一个可变文档权威身份。

快照与资源继承来源的敏感级别。来自私有 evidence 的摘录或快照只能位于 `.private/references/` 等私有路径，不能因文件名叫 references 就变为可发布资料；公开 references 只存经允许分享的材料。附件不单独绕过 thought 的检索范围进入全局摘要或搜索。

私有证据不登记为公开附件；记录不得包含服务密钥、认证 token 或带凭据 URL。第一版配置默认上限固定为：单资源 32 MiB、单次备份/导入解包总量 512 MiB、包内文件数 5,000、目录深度 16、最大解压膨胀比 100。超限拒绝，不静默截断；管理员可在服务配置中显式调整并记录。首版资源类型白名单为 PNG/JPEG/GIF/WebP、PDF、UTF-8 文本与 Markdown；扩展名和内容嗅探必须一致。SVG/HTML、可执行文件和脚本不以内联方式呈现；需要收录其他类型时只允许显式开启的附件下载模式，不在浏览器执行。用户文件名只作为显示值，存储文件名由资源 ID 与安全扩展名生成，禁止路径分隔符、控制字符和保留名称。私有证据文件使用 0600，私有目录使用 0700，不能依赖点号目录名提供保护。

## 7 位置解析与发现机制

### 7.1 统一接口

新增 `ObjectLocator`，至少提供：

```go
type ObjectLocator interface {
    Resolve(ctx context.Context, objectID string) (ObjectLocation, error)
    ListActive(ctx context.Context) ([]ObjectLocation, error)
    ListOwned(ctx context.Context, topicID string) ([]ObjectLocation, error)
}
```

所有 Markdown 读写、文档 Store、证据读取、资源解析、搜索重建、Topic 匹配、删除、恢复和 Git 路径生成使用同一结果。禁止新代码继续按 ID 日期推导实际路径。

未登记的旧对象只允许在启动迁移/显式扫描阶段发现，不在任意读取请求中随意“猜一个路径”。启动枚举旧 thoughts 路径和专题自描述文件，验证 ID 与内容一致后建立位置记录。

### 7.2 权威与重建

- `.content-registry/objects/<id>.json` 是运行时唯一有效的位置提交记录。
- 专题 `storage.snapshot.json` 是便携备份用的自描述投影，包含所属对象、路径、位置 generation 和必要哈希；不是活跃路由源。
- 普通重启直接读取登记，不依赖先找到 thought 才能知道它在哪里。
- 登记丢失时，修复工具综合扫描旧布局、专题快照、迁移提交记录与实际哈希。只有唯一且一致的对象才能自动恢复。
- 同 ID 多位置无法确定提交状态时，报告 `location_ambiguous`，对象暂不可写；不得按更新时间、文件大小或扫描先后猜测胜者。
- 独立专题备份导入另一个工作区时，根据备份清单重新登记。已有同 ID 不同内容时拒绝冲突，不默默覆盖；复制为新对象必须是单独的显式导入模式。

全局登记不参与普通公开交付包；完整工作区备份应包含它。单专题完整备份使用自描述快照提供导入所需信息。

### 7.3 链接与不可变历史

- 新生成文档采用稳定对象/资源标识，由应用解析到当前位置；导出时生成包内相对链接。
- 历史正文不因迁移改写。迁移记录保存旧资源解析基址及资源映射，解析器在读取旧 revision 时使用它。
- 外部 HTTP(S) 来源不因文件迁移自动下载或修改。
- 原始内容哈希保持不变；如果分享导出需要改写链接，导出件是派生产物，清单记录原 revision/hash 与导出 hash，不能冒充原 revision 字节完全一致。
- 单文档“精确正文导出”继续保留原行为；“可移植专题交付包”明确是另一种导出类型。

### 7.4 稳定资源链接与校验接入

本版新增应用内链接语法 `resource://<resource-id>`，只允许安全 ID，不允许主机、用户信息、路径穿越、查询参数或任意文件路径。该语法是待实现能力；现有最终正文校验只支持受限 HTTP(S)、thought 和 topic locator，不能仅改渲染器便宣称附件链路可用。

每个 revision 的资源解析以不可变映射为依据，映射记录资源 ID、内容哈希、敏感级别和受控位置引用。位置记录增加 `resource_bindings_ref`，包含映射清单的工作区相对路径与 SHA-256；迁移可以新增解析映射版本，但不得改写旧正文或原 revision manifest。映射必须在位置提交前持久化、验证可解析，且旧映射按历史读取需要保留。原件资源更新必须产生新资源版本或 ID，不能让固定文档无提示地指向新字节。

后端 AST 校验、validator 版本及 validation key、前端链接安全过滤、正文渲染、资源读取接口和专题导出必须一起接入：链接或图片引用的 ID 必须存在于所选 revision 的映射中，且文件哈希吻合。前端把合法资源引用转换为受控资源请求，不交给浏览器按未知 scheme 执行；不存在的资源显示明确错误。旧定稿的精确正文导出仍返回原字节，不因新增解析器改变历史内容。

`GET /api/resources/:resource_id/content` 不接受任意文件路径，不提供私有 evidence 的通用下载旁路；公开呈现只允许对应文档上下文中可呈现的资源。私有证据只走明确的本地审阅或完整备份流程。不得为支持附件而放开所有相对路径或所有 URL scheme。T22 与 T24 同时验证资源 scheme、缺失映射、错误哈希、跨文档越界引用与私有资源隔离。

## 8 写入与并发模型

### 8.1 支持边界

只支持单进程、单工作区写者。服务启动取得工作区写者锁，第二个写者拒绝启动写模式。外部编辑器的直接修改属于受检测的外部变更，不视为与服务事务同步的合法并发写入。

所有位置、策略、目录操作由统一 Mutation Coordinator 协调：

- 普通对象写入先取得工作区命名空间读锁，再取得稳定 object ID 锁。
- 迁移的计划快照和提交关键段、归属修改、检索策略切换取得命名空间写锁，再按 object ID 排序取得对象锁。长时间复制不持有全局写锁，提交前重新验证源 generation、源哈希和资源清单。
- 多对象操作按固定顺序取锁，不从 document 锁反向获取 topic 锁。
- 锁键是工作区身份和 object ID，而不是会变化的文件路径或 Store.Root。
- 远端模型调用、网络下载不在命名空间写锁内执行；先生成候选，再以预期 generation 提交。

目录本身使用 `catalog.generation` 的 CAS；专题配置和对象位置分别有自己的 generation。客户端必须提交所修改实体的期望版本。

所有会改变有效检索候选集合的操作必须在同一个命名空间写锁内更新事实、刷新策略快照并递增 epoch：工作区默认值、专题默认值与入口可见性、thought 覆盖值、owner 迁移、catalog 绑定/解绑、对象创建/删除/恢复、快照纳入/移除。不能只为显式搜索开关递增 epoch。普通正文变更不改变范围，但必须递增内容索引 generation，避免旧摘要缓存。

### 8.2 原子性承诺

不声称跨多个文件的 rename 自动构成事务。

- 单份权威 JSON/YAML 更新：临时文件写入、fsync、原子 rename、目录 fsync。
- 对象迁移：以位置记录的原子替换为权威提交点。
- 目录派生视图、搜索和反链可在提交后恢复，但必须展示 `pending/reconciling`，不能在未完成时返回“全部完成”。
- 多条目录编辑作为一次完整 catalog CAS 提交；第一版批量迁移按单对象操作执行，批次汇总成功/失败，不承诺全批次原子。

### 8.3 幂等性

所有迁移、发布快照、导出任务以及批量目录变更携带 `operation_id` 和请求哈希。

- 同一 ID 与相同请求返回已存在结果。
- 同一 ID 与不同请求返回 409。
- 请求超时后客户端查询操作状态，不创建新 ID 重复执行。
- 只读索引重建可重跑，结果必须确定性且不改人工内容。

## 9 加入 迁入 删除与恢复

### 9.1 加入专题

只修改 catalog，不移动文件、不改 owner、不触发 weave。校验对象存在、重复绑定、分组有效及版本策略。加入引用不继承目标专题默认检索策略。

### 9.2 新建专题文档

从 collection 页面创建计划条目后，可选择 profile 新建 thought：先生成 object ID 和位置预留，写入目标专题路径，验证成功后提交登记，再绑定 catalog。崩溃未完成的预留不可作为正式成员或完成文档展示；恢复流程完成绑定或回收未提交文件。

标题或 profile 必填输入尚未齐全时可保留草稿，不能伪造正式定稿。复用既有文档校验与定稿流程，不旁路创建 finalization。

### 9.3 迁入预览

必须先返回：源/目标位置、对象和资源清单、总大小、共享资源处理、owner 与有效检索策略变化、受影响链接、冲突和缺失文件、目标存储可用性。

用户执行“迁入”时使用该预览 ID、对象 generation 和计划哈希；过期预览不得继续执行。当前文档请求只设计此流程，不执行迁移。

### 9.4 迁移状态机

`planned → staging → verified → location_committed → reconciled → completed`

失败状态：`failed_before_commit`、`recovery_required`、`rolled_back`。

1. **planned**：持久化操作与文件清单。记录旧位置、旧 generation、owner、旧解析基址。
2. **staging**：在目标同文件系统 staging 目录复制文件，保留必要权限；源仍权威。私有证据保持私有权限。
3. **verified**：重新取得提交锁，核对预期源 generation，校验每个文件的长度与哈希、文档修订链、profile 快照、HEAD、引用资源完整性。迁移涉及的资源清单、历史相对链接基址和必要映射必须先持久化为本操作不可变版本，并能按候选位置完整解析；位置记录引用该版本。不能把必要资源映射留到提交后再补。目标正式目录如已存在冲突内容则停止。
4. **location_committed**：将完整 staging 提升为目标正式目录，然后原子更新位置记录，写入新 generation 和 operation ID。位置记录替换是提交点。目标就绪但尚未提交的文件不能被扫描为另一个活动原件。
5. **reconciled**：修复目录派生 pending 状态、反链、搜索与索引投影，发布持久化事件。所需权威资源与历史基址映射已经在 verified 阶段持久化，不属于此阶段补写内容。
6. **completed**：执行源副本隔离清理策略后完成；记录原路径退役，避免后续扫描再次导入。

迁移提交关键段持有固定 ID 写锁，阻止服务内正文写入；staging 复制阶段允许其他对象正常写入。若源对象在复制期间发生变化，提交时 generation 或哈希校验失败，旧计划必须重新预览。对外部编辑同样核对源哈希，不能覆盖外部修改。文件下载等长读者使用对象位置读租约；退役源必须等所有旧位置读租约释放后才能清理，不能仅依赖内存路径已切换。

### 9.5 崩溃恢复与回退

| 崩溃点 | 恢复判断 | 处理 |
|---|---|---|
| staging 未完成 | 位置仍指向源 | 保持源可读；清理或续传同 operation staging |
| 目标已就绪但位置未提交 | 源位置记录仍旧 generation | 不激活目标；核验后重试提交或撤销目标 |
| 位置已提交但日志未更新 | 位置记录含 operation ID 和新 generation | 认定已提交，继续投影恢复；不得再次从源复制覆盖 |
| 搜索未更新 | 位置已提交，派生版本落后 | 查询以当前位置/策略为准；重建派生索引 |
| 源清理未完成 | 目标为权威，源已退役 | 后台清理隔离副本，不再次导入 |
| 位置与文件哈希冲突 | 无法证明完整性 | 标记 recovery_required、禁止写入、提供诊断，不猜测恢复 |

所有权迁移不隐式增加或移除 catalog 条目。已有条目继续按稳定 ID 解析；“迁入并加入目录”的组合操作以持久化父操作顺序执行迁移和目录 CAS，分别记录结果，遇到目录冲突时返回“文件迁入已完成、目录待处理”，不得谎称全成功或擅自逆转已提交迁移。

第一版旧副本默认移入工作区私有恢复区保留 7 天；恢复区不得被正常扫描、Git 或导出收录。容量不足必须在预览中提示或阻止迁移，不通过立即删除源降低安全性。永久清理需符合产品明确的清理策略与用户操作约束。

提交后的“回退迁移”是新的迁移操作，必须确认对象没有未处理冲突，并把当前完整数据迁回；不能简单恢复旧位置 JSON 指向陈旧源副本。

### 9.6 删除与解绑

- 移除目录条目：只解除关联；若该 thought 的 owner 仍是该专题，文件仍保留，归入“未编入目录的所属文档”列表。不能因移除索引就丢失文件管理入口。
- 删除 thought：走原对象删除语义，位置记录标记删除；collection 条目保留为“来源已删除”，直到用户显式移除或重新绑定。
- 删除专题：第一版仅允许删除无主归属对象且无进行中操作的空专题。非空专题提示迁出/备份后再删除，不能递归删除整个目录。
- 删除共享资源：先验证全部引用；不能仅检查一个条目。
- 恢复对象：恢复原身份和版本链，重新核验位置冲突，相关缺失条目可以重新解析。

## 10 专题索引与状态

### 10.1 索引内容

专题首页包含：说明、总体设计入口、分组目录、待处理项、最近活动、成员/必需条目统计。

每个条目显示：展示标题、原文档标题、profile、所属分组、必需性、版本选择、工作 revision、选中定稿 revision、交付等级、校验新鲜度、来源可用性、最后更新时间。

操作分别命名“查看定稿”“查看草稿”“查看问题”“查看文件位置”，不使用一个按钮隐式选择版本。

### 10.2 状态分维度

| 维度 | 状态 |
|---|---|
| 绑定 | planned、bound、missing、snapshot |
| 文档交付 | ordinary_note、not_finalized、proposal、ready_for_use |
| 工作进展 | no_new_draft、new_draft、validation_running、validation_failed、needs_review |
| 版本选择 | latest_finalized、pinned_finalized、pinned_unavailable |
| 派生数据 | current、reconciling、failed |

例如：“已有 ready_for_use 定稿；存在新版草稿；当前查看固定旧定稿”是合法组合。

校验状态必须与所展示 revision 和 validation key 对应。不能将旧报告的 passed 标记附在新草稿上。普通 note、历史 v1 profile 和未绑定计划项不可被推断为 ready_for_use。

### 10.3 完成度

分母为 `required=true` 的条目数。分子为已绑定、选中版本确实已定稿、达到专题交付等级且没有可用性阻断的必需条目数。

- 分母为零时显示“未配置必需条目”，不显示 100%。
- 普通辅助 note 不因已保存计入设计交付完成。
- 来源被删时条目仍在分母。
- 新草稿不抹去旧定稿，但单独提示待处理更新。
- “已交付率”不是开发完成率。

### 10.4 确定性生成与活动

`catalog.json + topic配置 + 当前对象事实` 生成 UI 数据和 `catalog.md`。生成器固定排序和格式；相同输入产生相同内容哈希。文件顶部标注生成来源和输入版本，不鼓励直接编辑生成文件。

人工 index.md 不被生成器覆盖。目录投影失败不回滚已经成功提交的目录事实，显示失败并允许重建。

持久化活动记录至少含 actor 类型、operation ID、实体 ID、前后 generation、时间和变化摘要；日志不得包含私有证据正文。SSE 是通知通道，不是事实存储或唯一恢复机制。

## 11 全局检索参与策略

### 11.1 有效策略

```text
effective_global(thought):
  include -> true
  exclude -> false
  inherit + owner topic -> owner.owned_thoughts_global_default
  inherit + no owner -> workspace.global_search_default
```

工作区默认值为 true。已有数据缺省是 inherit，旧专题缺省为 true。

只有主归属专题影响继承值；其他引用专题不改变结果。改变 owner 时必须在迁移预览显示策略变化。

专题入口本身由 `topic_global_visible` 控制。它不授权把被排除成员的正文、标题列表或摘要重新拼进全局检索文本。collection 的全局索引只使用用户确认的专题名称和简介；成员目录留在专题范围内。

跨专题只读快照第一版不作为独立全局 thought 索引，避免重复召回和绕过原件策略。它可在显式专题范围检索，结果标明快照及来源版本。

### 11.2 范围语义

| 请求范围 | thought 全局参与策略 | 成员限制 | 结果 |
|---|---|---|---|
| global，无 topic_id | 应用 | 无 | 全局可见对象 |
| global，有 topic_id | 应用 | 同时应用 | 全局可见对象在指定专题内的子集 |
| topic，有 topic_id | 不应用全局参与开关 | 必须应用 | 指定专题关联对象与明确纳入的快照 |
| topic，无 topic_id | 不执行 | 无 | 400 参数错误 |

客户端显式传 `scope`；省略时按 `global` 解释。`scope=topic` 必须带 `topic_id`。旧客户端仅传 `topic_id` 的兼容归一化已由用户豁免。

全局排除不影响索引维护、专题内检索、直接文档读取、正常导出或既有公开开关；它不是保密机制。是否可调用模型和生成 embedding 属于另一个策略域，本设计不新增其权限。

### 11.3 所有结果入口一致

必须覆盖关键词查询、内部向量召回、混合融合、重排、候选推荐补充、自动补全、全局卡片摘要、计数、分页与缓存。搜索候选的独立调用不能绕过策略。未归档 session 等非 thought 候选沿用其类型规则，不能借其摘要重新呈现被排除 thought 的派生正文。

## 12 检索实现与一致性

### 12.1 数据与查询

搜索库可保存 owner 和覆盖值的派生列，另维护专题策略映射；权威来源仍是位置记录与 topic 配置。不得把 `false` 与字段未提供混为一谈。

召回过程：

1. 对输入完成参数校验；query embedding 如需要可在锁外计算。
2. 取得命名空间读锁，读取当前策略 epoch 与有效候选集合。若搜索库的策略派生列落后于权威版本，使用当前策略快照构造临时 eligibility 表或精确 ID 过滤，不继续使用陈旧预过滤列。
3. 在召回和 top-k 之前应用范围条件。
4. 关键词与向量支路使用同一个候选集合；混合后再排序和分页。
5. 结果、数量、候选补充和摘要在同一策略快照下生成。
6. 响应发送前确认策略快照仍有效；若跨越锁边界发生变化，必须重建候选集合并重新执行召回、融合、排序、分页、计数和摘要生成，不能只过滤旧候选或隐藏部分结果后返回不足额结果。

第一版可以使用较保守的读锁覆盖数据库查询以保证一致性；不要在锁内调用远端模型。后续优化可以使用 immutable eligibility snapshot 和版本重试，不能削弱行为。

ANN 引擎不能可靠在过滤集合内求 top-k 时，退回符合范围的精确向量扫描，或实现可证明正确的补召回。不接受“先取全局 top-k 再删掉隐藏结果”的实现。允许性能降级，但不得把应返回的可见结果全部丢掉。

### 12.2 变更与缓存

- 策略修改在命名空间写锁内提交权威文件，更新进程内策略快照并递增 epoch 后才返回成功。
- 缓存键包含进程实例标识、scope、topic_id、query、排序分页、策略 epoch、索引 generation。
- 重启清空旧进程缓存并重建策略快照；不能只依赖内存 epoch 在不同启动间唯一。
- 旧向量或旧全文索引可继续作为内容候选，但结果必须受当前权威策略约束。
- 恢复任务未完成时，不确定策略按排除处理并显示索引降级，不能默认开放。
- 修改 owner 默认值应影响所有 inherit 对象，但不改变 include/exclude 的显式覆盖。

### 12.3 检索不扩权

不提供 `include_hidden=true` 作为无说明的全局绕过。用户需要在专题中检索时使用显式 topic scope。鉴权未来增加时，权限过滤必须先于或与检索范围联合应用；本设计不替代鉴权。

## 13 API 合约

下表为**拟新增或扩展接口**，不是当前已存在路由清单。继续沿用项目的统一响应和错误包装。

| 方法与路径 | 用途 | 并发和幂等要求 |
|---|---|---|
| POST /api/topics | 扩展 mode、search_policy 创建 | operation_id；同 ID 不同请求冲突 |
| PATCH /api/topics/:id/settings | 修改 collection 配置 | expected_generation |
| GET /api/topics/:id/catalog | 读取权威目录与派生视图版本 | 返回 generation、projection_status |
| PUT /api/topics/:id/catalog | 原子替换编排结果 | expected_generation、operation_id |
| POST /api/topics/:id/entries | 计划条目或绑定对象 | catalog CAS；重复绑定拒绝 |
| DELETE /api/topics/:id/entries/:entry_id | 解除目录关联 | catalog CAS；不删除 thought |
| GET /api/thoughts/:id/placement | 查看归属与实际位置 | 相对路径；不泄漏工作区绝对路径 |
| PATCH /api/thoughts/:id/search-policy | 修改 inherit/include/exclude | object generation；同步策略生效 |
| POST /api/topics/:id/migrations/preview | 迁入或迁出预览 | 固定来源 generation 和计划哈希 |
| POST /api/topics/:id/migrations | 执行已确认计划 | preview_id、operation_id |
| GET /api/operations/:operation_id | 查询结果或恢复状态 | 只读 |
| POST /api/topics/:id/index/rebuild | 重建派生目录 | 可幂等；不改人工 index.md |
| GET /api/search | 扩展 scope 和兼容 topic_id | 同一策略作用全部结果支路 |
| POST /api/topics/:id/exports | 交付包或备份任务 | 明确 export_kind、版本选择、私有证据选项 |
| GET /api/topics/:id/exports/:export_id | 导出状态与受控下载 | 不返回任意磁盘路径 |
| POST /api/topics/:id/entries/:entry_id/drafts | 从计划条目创建带 profile 的 thought 草稿 | object 创建 operation_id 与 catalog CAS 分步记录 |
| POST /api/topics/:id/snapshots | 创建已选版本的只读引用快照 | 来源版本、哈希、敏感级别与 operation_id |
| POST /api/topics/:id/resources | 登记并上传允许的附件 | 上限/类型检查、operation_id；禁止任意路径 |
| GET /api/resources/:resource_id/content | 受控读取资源 | locator 解析、归属/敏感性检查，禁止越界 |
| POST /api/operations/:operation_id/retry | 恢复允许重试的失败步骤 | 同一请求语义；不可改目标路径 |
| POST /api/objects/:id/migrations/reverse-preview | 生成反向迁移的新计划 | 当前 object generation，不能恢复陈旧登记 |
| POST /api/topics/:id/mode-conversions/preview | 预览 aggregate/collection 转换 | 固定配置与目录 generation |
| POST /api/topics/:id/mode-conversions | 执行模式转换 | preview_id、operation_id、保留原正文 |
| POST /api/topic-imports | 导入专题备份到 staging | operation_id、包清单校验、分对象可恢复提交 |


目录写入示例：

```json
{
  "operation_id": "op-catalog-12",
  "expected_generation": 12,
  "groups": [{"id": "g-core", "title": "核心功能"}],
  "entries": [{
    "id": "e-routing",
    "group_id": "g-core",
    "title": "请求路由设计",
    "role": "design",
    "required": true,
    "binding": {"kind": "thought", "thought_id": "20261023-routing", "version_policy": "latest_finalized"}
  }]
}
```

迁移执行示例：

```json
{
  "operation_id": "op-move-routing",
  "preview_id": "preview-routing-4",
  "expected_object_generation": 4,
  "plan_hash": "sha256:verified-plan"
}
```

接口不能接受客户端自选绝对 source/destination 路径、跳过验证标志或任意 Git 路径。

主要错误：

| HTTP | 代码 | 语义 |
|---|---|---|
| 400 | invalid_scope / invalid_binding | 参数组合不合法 |
| 404 | topic_not_found / object_not_found | 对象不存在 |
| 409 | generation_conflict | 乐观并发冲突，需重新读取 |
| 409 | operation_conflict | 相同操作 ID 对应不同请求 |
| 409 | ownership_conflict / location_ambiguous | 归属冲突或位置不可确定 |
| 409 | migration_in_progress | 对象正在迁移，禁止冲突操作 |
| 422 | required_revision_unavailable | 固定版本或交付要求无法满足 |
| 422 | resource_incomplete / unsafe_path | 资源不完整或路径非法 |
| 503 | recovery_required | 需要先恢复再操作 |

任何 accepted/202 响应仅表示任务已接受，不表示迁移或导出完成。

## 14 专题查看与管理体验

本章落实“方便查看”的明确要求。首版的默认体验应让用户三步完成主要任务：**找到专题 → 在分组目录中选文档 → 保持专题上下文连续阅读**，不要求反复打开抽屉、回到全局列表或重新搜索。

### 14.1 查找与进入专题

左侧主导航提供“专题”。专题列表支持按名称/简介过滤，展示名称、所属文档数、必需条目交付情况、最近变更及待处理项数量。提供“最近查看”和“收藏”作为个人导航偏好，不作为业务完成状态或自动全局检索结果。

专题入口关闭全局检索后，用户仍可从专题列表、收藏或已知链接进入；这一行为必须在设置帮助中说明。全局搜索过滤不能被错误复用为专题管理列表过滤。

用户打开某个 collection 后默认进入“文档”页，聚合模式仍保留原默认体验。专题页顶部固定展示专题名称、说明、文档/综述/活动/设置入口，不因切换成员文档而丢失专题身份。

### 14.2 默认专题首页

首次进入专题显示概览与分组目录：

- 简介、总体设计入口、推荐阅读起点。
- 按 catalog 顺序排列的分组和条目。
- 简洁统计：“必需 10 项，已交付 6 项，缺失 2 项，有新版草稿 3 项”，注明交付门槛。
- “待处理”快捷过滤：计划未创建、无定稿、校验失败、待人工复核、定稿后有更新、来源缺失。
- 最近变更仅作为辅助，不用更新时间打乱人工确定的阅读顺序。

进入已有浏览记录的专题时可恢复上次成员和滚动位置，但需要醒目标识当前文档；提供“返回专题概览”，不能让用户误以为进入了其他专题。

### 14.3 桌面连续阅读布局

采用三部分布局：

1. **左侧专题目录**：分组树、条目状态、当前条目高亮；支持展开/折叠、目录内关键词过滤和待处理过滤。
2. **中间文档正文**：显示选定 thought 的具体 revision，正文顶部展示标题、草稿/定稿、交付等级和版本来源；正文下提供“上一篇”“下一篇”。
3. **可折叠右侧信息区**：本篇文档目录、profile、校验摘要、版本选择、所属专题及来源信息。

“专题目录”与“本篇文档目录”必须分别命名：前者切换文档，后者跳转当前文档的标题锚点。避免两个都叫“目录”造成混淆。

上一篇/下一篇按正式 catalog 顺序跨分组移动。第一版只对当前筛选后可阅读的条目导航，并显示“当前为筛选结果中的第 N 项”；清除筛选后恢复完整顺序。计划/缺失条目不作为可阅读文档，但目录中保留位置并提供处理入口。

中间正文可完整阅读，不以搜索命中片段或摘要替代。正文切换不强制关闭左侧分组，不跳回专题列表。

### 14.4 默认版本与草稿查看

- `latest_finalized` 条目默认展示当前最近定稿。
- `pinned_finalized` 条目展示固定定稿，并提示“已固定版本”，可另行查看更新但不改绑定。
- 尚无定稿的条目默认展示“未定稿”说明和“查看草稿”动作；用户显式点击后可阅读草稿，正文顶部持续显示“草稿，未作为最终交付”。
- 草稿深链接允许直接打开，但必须有同样的草稿标识。
- 已有定稿又有新版草稿时，默认不静默切到草稿；提供“存在新版草稿”入口。
- 版本切换保留专题和条目上下文，不修改目录版本策略。修改固定版本必须通过目录编辑与 CAS。
- 文档删除或固定 revision 不可用时，显示带条目名称的缺失状态及重新绑定/恢复入口，不自动打开其他文档，也不将原草稿顶替定稿。

### 14.5 稳定链接与浏览状态

沿用现有 SPA `#/topics/<id>` 路由形态，扩展而不另造文件路径路由。建议：

```text
#/topics/<topic-id>?tab=documents&entry=<entry-id>&view=final&revision=<revision-id>
#/topics/<topic-id>?tab=documents&entry=<entry-id>&view=draft
#/topics/<topic-id>?tab=documents&entry=<entry-id>&view=final&revision=<revision-id>&section=<anchor>
```

参数只接受安全的 topic/entry/revision ID 与已解析锚点。服务验证 entry 属于该专题、revision 属于绑定文档；不能通过 URL 填写任意磁盘路径。

有 revision 的链接固定该版本；无 revision 的 final 链接按条目当前版本策略解析。界面“复制链接”默认复制当前正在阅读的确切版本，另提供“复制跟随专题版本链接”，二者说明不同。

刷新页面、浏览器前进/后退应恢复专题、条目、版本、正文锚点与筛选条件。个人展开状态、滚动位置和收藏可保存在本地偏好，键至少包含 workspace/topic/entry/view/revision，不能把 A 文档滚动位置套给 B。

目录条目改标题或排序后，基于 entry ID 的深链接仍有效；条目被删除时展示“条目已移除”并提供专题入口，不用旧序号打开另一篇文档。

这些是应用导航链接，不是公开分享授权。本地部署地址和私有工作区链接不能保证其他人可打开；只有走既有明确的分享/导出流程才能承诺分享结果。

### 14.6 专题内定位与检索

区分两种控件：

- **筛选目录**：只在当前 catalog 标题、角色和状态中定位，不调用全文搜索。
- **搜索本专题内容**：明确使用 `scope=topic`，结果标注所属分组、草稿/定稿或快照版本；打开后仍在专题阅读视图内。

全文搜索命中版本必须明确。第一版若搜索索引针对工作正文，则结果标识“工作版本命中”；不能把工作版本片段挂在定稿按钮上。查看定稿时可提示该片段未必存在于所选定稿。后续版本化搜索可扩展，但不能用模糊 UI 掩盖版本差异。

文档页面提供“所在专题”反链，用户可从独立 thought 返回相应专题条目。多专题时列出可选择的关联，不替用户猜一个 owner 就覆盖当前浏览上下文。

### 14.7 管理与查看分离

普通查看默认不显示拖拽手柄和破坏性操作。点击“编排目录”进入编辑态，支持新增分组、排序、计划条目、批量加入和移除。保存整个编排结果使用 catalog CAS；冲突时保留本地未保存内容，提供重新读取/比较，不覆盖其他编辑。

从条目创建文档时预填主归属和 profile 建议，但用户仍确认 profile 及必填输入。迁入、仅引用和保存快照使用不同动作和说明，不在浏览过程中自动移动文件。

专题设置显示实际物理目录的工作区相对路径；“查看文件位置”不赋予浏览任意服务器目录的能力。

### 14.8 窄屏与可访问性

窄屏使用单正文布局，专题目录放在抽屉内，顶部保留专题名、当前文档名和打开目录入口；上一篇/下一篇继续可用。右侧本篇目录与元信息折叠，不挤压正文宽度。

分组折叠、条目选择和版本切换均可键盘操作，当前项有可识别焦点和 aria 状态；不能只靠颜色区分草稿/定稿。状态徽标有文本，加载失败提供重试，空专题提供创建计划或加入文档入口。

### 14.9 异步正确性

复用现有 routeApplySerial 的思路，增加 topic/entry/revision 选择 epoch。快速切换 A→B→A 时，旧请求即使返回同一个 ID，也必须因旧 epoch 被丢弃。

正文、版本元信息、校验摘要和活动各自请求都绑定当前作用域；取消请求只作优化，不能依赖取消一定成功。旧响应不得覆盖当前文档正文、标题、滚动位置或状态。加载时不保留上一篇正文却换成本篇标题，应显示明确骨架或完整旧页加切换状态。

### 14.10 未编目所属文档与问题处理

专题中 owner 为本专题但未在 catalog 的对象，显示在“未编目文档”管理区，可重新加入或迁出。缺失条目提供恢复、重新绑定、改为计划项或移除动作，不直接从统计中消失。

所有错误页保留专题导航和用户上下文。权限或资源不可用错误不能转换为“空白文档”或“暂无内容”的成功状态。

## 15 Git 安全与导出备份

### 15.1 Git

继续使用精确路径白名单，不执行“对整个专题目录 git add”。允许：专题配置、catalog、生成索引、人工综述、正式内容及按资源策略允许的文件。禁止：`.private`、staging、恢复区、运行日志、凭据和未确认分享的来源摘录。

位置记录包含工作区相对路径，无机器绝对路径。设计确定将 `.content-registry/schema.json` 和已提交的 `.content-registry/objects/<安全ID>.json` 纳入工作区本地 Git 精确白名单，以便恢复路由；未提交 intent、私有迁移日志、staging 和恢复副本全部排除。登记记录不包含私有证据正文、凭据或机器绝对路径，也不随单专题公开交付包导出。专题变化生成的提交路径通过 locator 解析，删除/迁移必须同时记录源退役路径与新路径。

更新现有忽略规则时保留用户规则；不能仅依赖 `.gitignore` 保密，显式白名单是最后提交边界。测试必须放置私有 canary 文件验证不会进入待提交列表。

### 15.2 两类输出

导出采用显式文件清单枚举和逐项安全校验，不递归打包整个专题目录，不跟随符号链接。

**交付包**：确定的专题目录、所选定稿正文、允许分享的资源、来源版本与哈希清单、包内可用链接。默认不包含草稿、完整审阅过程、私有证据、运行数据。缺少允许公开的必要附件时阻止“完整交付包”，或只能生成明确标记为不完整的预览包。

**完整备份**：目录、归属、自描述位置、thought、版本链、报告、定稿、资源及显式选择的私有证据。包含私有信息的备份必须明确标记，不复用公开分享下载入口。是否加密由备份配置和用户选择决定，不能假定本地 ZIP 天然安全。

没有私有证据的交付包可以阅读，但不一定具备完整离线证据复核能力；清单必须说明这一点。

导出任务启动时冻结本次选择清单，记录 topic/catalog generation、具体 revision、资源版本与哈希；不在打包循环中反复解析 latest。复制不可变 revision 文件后核验哈希；可变 thought 投影、配置和资源清单必须使用受锁保护的快照副本，或复制后重新核对 generation 与哈希，变化则重试。私有证据亦按固定哈希读取。生成 storage.snapshot.json 前核对当前位置记录，不能直接把陈旧派生文件打进备份。任务结果明示冻结时间与选择版本，因此后续正常编辑不改变已完成导出。

### 15.3 导入

校验包清单、内容哈希、相对路径、符号链接、文件数量与总大小，防止路径穿越和解压炸弹。导入先 staging，验证后按对象逐一原子登记；ID 冲突默认拒绝。包级导入是可续跑的父操作，不承诺跨全部对象原子：清单记录每个对象的未提交/已提交/失败状态，部分成功时返回明细，重试只处理未完成对象。导入对象位置记录先标记 lifecycle=import_pending，保留管理/诊断可见性，但不进入正常列表、全局/专题检索或定稿操作；全部必要对象验证成功后，按持久化父操作依次激活并最后激活专题 catalog，进程恢复继续完成未结束的激活步骤。对外在父操作 completed 前始终展示导入中，不承诺批量激活的瞬时原子性。失败专题保持 import_pending，不宣称完整恢复。完整备份应能在空工作区重建对象位置及专题目录，不依赖原机器绝对路径。

未获得外部来源复制权限或未选择下载的资料只保留引用与缺失状态，不声称已全部归档。

## 16 事件 恢复与观测

新增/扩展事件：`topic.catalog_updated`、`topic.search_policy_updated`、`thought.search_policy_updated`、`object.placement_changed`、`topic.index_updated`、`topic.migration_completed`、`topic.migration_failed`。

订阅已有文档 revision/validation/finalized 变化以刷新状态；现有 Topic 模块没有完整覆盖文档生命周期事件，需补接线。事件包含对象 ID、topic ID、generation、operation ID，不含私有正文。

提交顺序是“持久事实 → 持久事件/outbox → 异步投影 → SSE 通知”。第一版可用轻量持久 outbox，不依赖消息队列。消费使用实体 generation 幂等，乱序旧事件不能覆盖新状态。所有 catalog、topic 配置、位置、策略、资源登记及删除/恢复元数据变更，都必须先写持久 prepared intent，包含 operation ID、目标实体、预期 generation、候选后值哈希及事件类型，再提交权威文件；这不只适用于迁移。若事实提交后 outbox 尚未写入即崩溃，启动通过事实的 operation ID/generation 与 intent 核对，补写事件并重建派生投影；事实未提交的 intent 撤销或按同一请求恢复。启动同时执行全量 generation 对账作为兜底，不依赖曾经收到过 SSE。

启动恢复先于正常写服务：检查迁移日志与位置记录、隔离未提交 staging、重建策略快照、修复目录投影，再开放写操作。搜索可以在重建期降级但不得忽略排除规则。

最少指标：迁移阶段耗时与失败数、位置冲突数、catalog generation、投影延迟、索引重建失败、有效策略过滤数量、缓存 epoch 重试、资源缺失数。日志中不输出私有证据和完整正文。

## 17 第二阶段依赖与发布基线

### 17.1 依赖复核

依赖关系绑定条目 ID 与上次复核的源 revision/hash。源版本变化时标记“待复核”，不自动断言不兼容，也不自动把下游文档改为已验证。

第一版不强制建立依赖图；第二阶段先限制同专题有向无环依赖，检测循环。跨专题依赖通过稳定文档 ID 和固定版本表达，后续再扩展。

共享术语、接口约束和版本适用范围应有明确总体设计来源。确定性检查负责缺失和版本变化；语义冲突由人工/模型辅助评审，不声称自动证明所有内容一致。

### 17.2 专题发布

发布清单固定：专题配置 generation、catalog generation、条目顺序、文档 ID、revision ID、finalization ID、body/contract/profile/source-manifest 哈希、资源清单及检查结果。

发布不能保存 `latest_finalized` 这种会漂移的选择器。创建发布时把选择器解析成具体版本，核验后写入不可变清单。

查看或恢复旧专题基线不自动回退当前 owner、全局检索策略或访问配置；恢复内容与修改实时策略必须是分开的显式操作。

发布过程通过版本核对确保不是读取一半新、一半旧：收集快照后重新检查所有相关 generation，变化则重试或返回冲突。发布不需要阻止后续正常编辑，历史基线继续指向原版本。

专题 ready 状态不能仅由“每篇已定稿”推断，还必须检查必需条目、可用性、依赖复核及专题级约束。发布基线是第二阶段，不作为第一版文档组织能力已经具备的宣传。

## 18 实现拆分与发布顺序

| 阶段 | 交付内容 | 必须通过的门禁 |
|---|---|---|
| P0 基础设施 | 稳定 ID locator、位置登记、锁顺序、旧路径兼容、资源清单、私有白名单 | 旧功能回归、位置重建和冲突测试 |
| P1 文档集组织 | collection、catalog、计划项、分组排序、版本状态、确定性目录、UI | 目录并发与独立正文保护 |
| P2 物理归拢 | 新建即归属、迁移预览/执行/恢复、历史链接、备份导入 | 每个崩溃点恢复、无数据丢失及私有泄漏 |
| P3 检索控制 | 默认与覆盖、显式 scope、全部支路过滤、缓存/策略一致性 | 检索矩阵与高分隐藏结果测试 |
| P4 首版收口 | Git、导出、观测、启动恢复、端到端与文档 | U1–U5 全部满足后才发布正式功能 |
| 后续阶段 | 依赖复核、一致性评审、发布基线与差异 | 独立设计和验收，不阻断已明确的首版边界 |

P0–P4 是同一首版的开发顺序，不代表可只交 P1 就宣布三项能力完成。

推荐代码落点：

- `internal/pkg/models`：Topic 模式、catalog、placement、resource、policy DTO。
- 新 `internal/pkg/objectlocation`：解析、登记、迁移日志、恢复与锁协调。
- `internal/pkg/topicstore`：collection 清单与旧 aggregate 分支隔离。
- `internal/modules/topic/biz`：编排、状态聚合、事件与候选。
- `internal/pkg/markdown`、`documentstore`、`documentflow`：改用 locator，保留既有 revision 哈希和交付语义。
- `internal/pkg/searchdb`、`modules/search/biz`：范围与有效策略、重建及过滤。
- `internal/modules/application/thoughtflow/service`：接口和错误映射。
- `web`：专题索引、编排、迁移和搜索范围，双语 i18n。
- Git 模块与 workspace：精确路径、忽略规则和启动恢复。

## 19 验收矩阵

以下用例是实现验收要求，并非已执行通过。测试必须包含失败注入和重启恢复，不能只验证正常路径。

| ID | 验收场景 | 通过标准 | 需求 |
|---|---|---|---|
| T01 | 读取无 mode 旧 Topic | 正式豁免：历史数据兼容；现有 aggregate 功能另有回归 | U1 |
| T02 | 新建 collection | 不调用 weave，不改成员正文 | U1 |
| T03 | 多篇模块设计加入 | 独立 thought 和 revision 不被合并 | U1 |
| T04 | 同 thought 加入多个专题 | 只存在一个可编辑权威位置 | U1/U3 |
| T05 | 分组与排序 | 保存、重启、导出顺序一致 | U2 |
| T06 | 计划条目 | 无虚构 thought ID，显示缺失 | U2 |
| T07 | 重复绑定 | 同专题重复 thought 被拒绝 | U2 |
| T08 | 目录 CAS 冲突 | 409，后提交者不覆盖先提交者 | U2 |
| T09 | 幂等重放 | 相同请求不重复创建；不同请求同 ID 冲突 | U2/U3 |
| T10 | 新建专题文档 | 实际文件和版本位于专题根目录 | U3 |
| T11 | 加入仅引用 | owner 与路径均不改变 | U1/U3 |
| T12 | 迁入预览取消 | 不移动文件，不改检索策略 | U3/U4 |
| T13 | 完整迁入 | ID、revision 和内容哈希保持不变 | U3 |
| T14 | staging 中崩溃 | 源继续权威，无半成品被索引 | U3 |
| T15 | 目标完成但未提交崩溃 | 目标不被误认第二原件 | U3 |
| T16 | 位置提交后崩溃 | 恢复继续提交后阶段，不覆盖目标 | U3 |
| T17 | 源清理前崩溃 | 退役源不再次导入 | U3 |
| T18 | 外部编辑源文件 | 哈希变化阻止旧计划提交 | U3 |
| T19 | 空间不足或权限错误 | 保留源，报告可恢复失败 | U3 |
| T20 | 同 ID 多位置冲突 | fail closed，不猜测胜者 | U3 |
| T21 | locator 重建 | 唯一对象可恢复，歧义对象拒绝写入 | U3 |
| T22 | 历史相对链接 | 迁移后仍可解析，历史正文哈希不变 | U3 |
| T23 | 共享附件 | 迁移一篇不破坏其他引用 | U3 |
| T24 | 符号链接和路径穿越 | 读取、写入、导入、导出全部拒绝越界 | U3 |
| T25 | 普通 note 状态 | 不伪装为正式 ready_for_use；旧 profile 兼容正式豁免 | U2 |
| T26 | 已定稿后产生新草稿 | 同时显示旧定稿和新草稿状态 | U2 |
| T27 | 固定旧定稿 | 新定稿出现后不漂移 | U2 |
| T28 | 旧校验报告 | 不附加到新 revision 的状态上 | U2 |
| T29 | 必需项删除源 | 条目保留，完成率不虚增 | U2 |
| T30 | 移除所属条目 | 文件不删除，可在未编目列表找回 | U2/U3 |
| T31 | 非空专题删除 | 阻止递归误删并指引迁出 | U3 |
| T32 | 旧数据检索默认 | 正式豁免：历史数据升级；当前策略与重建保护仍须验证 | U4 |
| T33 | 专题默认关闭 | inherit thought 全局不返回，专题内可搜 | U4 |
| T34 | 显式 include/exclude | 优先于 owner 默认 | U4 |
| T35 | 多专题引用策略 | 非 owner 专题不能改变继承值 | U4 |
| T36 | owner 迁移 | 预览显示并在提交后应用新继承值 | U3/U4 |
| T37 | 关键词/语义/混合矩阵 | 所有已存在支路范围一致 | U4 |
| T38 | 隐藏高分结果占 top-k | 可见低分候选仍得到正确返回 | U4 |
| T39 | 缓存后切换策略 | 成功响应之后新请求不返回旧可见性结果 | U4 |
| T40 | 搜索候选与摘要 | 不绕过排除返回成员正文片段 | U4 |
| T41 | 专题入口可见成员隐藏 | 全局仅搜到允许的专题名称简介 | U4 |
| T42 | topic scope 参数 | 显式 topic 无 topic_id 报错；省略 scope 为 global；旧请求兼容正式豁免 | U4 |
| T43 | 索引重建与重启 | 全局排除仍生效，不因重建短暂开放 | U4 |
| T44 | 私有 canary 文件 | 不进入 Git 白名单与默认交付包 | U3 |
| T45 | 完整专题备份恢复 | 空工作区可重建归属、目录和版本链 | U3 |
| T46 | 交付包离线查看 | 允许资源链接可用；缺失明确披露 | U2/U3 |
| T47 | 人工 index.md | 目录更新、重建不覆盖其内容 | U2 |
| T48 | 乱序重复事件 | 旧 generation 不覆盖新投影 | U2/U4 |
| T49 | SSE 中断 | 重新读取能恢复真实状态，不靠通知维持事实 | U2 |
| T50 | 第二服务进程 | 拒绝写模式，不能形成双写者 | U3 |
| T51 | 受支持启动与降级流程 | 布局版本预检拒绝不兼容程序写入；明确不保证手工绕过启动器的旧二进制安全 | U3 |
| T52 | 检索排除的直接访问 | 按既有访问策略工作，UI 不误标为私密 | U4 |
| T53 | 三步查看主流程 | 列表进入专题、选目录、阅读正文无需跳回全局 | U5 |
| T54 | 分组折叠和上一篇下一篇 | 按明确筛选范围跨组导航，当前项持续高亮 | U5 |
| T55 | 刷新与前进后退 | 恢复 topic/entry/revision/anchor，不串文档状态 | U5 |
| T56 | 条目改名和重排 | 固定 entry 深链接不失效、不打开错误文档 | U5 |
| T57 | 无定稿与新版草稿 | 不静默伪装成 final，显式草稿动作与徽标 | U5 |
| T58 | 版本切换 | 保持专题上下文，不擅自改固定版本配置 | U5 |
| T59 | A→B→A 快速切换 | 旧 epoch 响应不能覆盖当前正文或元信息 | U5 |
| T60 | 目录筛选与正文搜索 | 两者区分，专题内结果打开后保持专题上下文 | U5 |
| T61 | 工作版本搜索命中 | 命中版本标识明确，不假冒定稿内容 | U5 |
| T62 | 窄屏和键盘阅读 | 目录抽屉、焦点、文本状态、连续阅读可用 | U5 |
| T63 | 缺失条目和加载错误 | 保留上下文，展示原因与处理入口，不默换文档 | U5 |
| T64 | 私有导航链接 | 不宣称复制应用链接即公开分享成功 | U5 |
| T65 | 未编目所属文档 | 移除目录后仍能管理其文件与归属 | U3/U5 |

测试层次：模型与 store 单元测试；locator/迁移故障注入；搜索 SQL 和向量路径测试；真实 API/SSE 集成；Web 编排与版本状态测试；Linux/macOS 文件系统兼容与 Git 白名单测试。不得用 mock-only 测试替代实际文件迁移和重启恢复。

## 20 收口判定与发布回退

### 20.1 正式收口标准

只有同时满足以下条件才标记功能完成：

1. U1–U5 均有实现与通过的验收证据，T01–T65 全部通过或有明确不适用理由；不能把关键未实现项记为不适用。
2. 新路径中的文档能被列表、读取、修改、删除、搜索、专题、Git 和导出完整识别。
3. 每个迁移崩溃点均验证可恢复，历史修订与定稿哈希未被改写。
4. 全局排除覆盖服务端全部已支持召回与附加结果，缓存和重启不破坏策略。
5. 私有证据不进入普通提交和交付包；备份恢复能力有真实验证。
6. 旧 aggregate 行为与现有文档交付流程回归通过。
7. 用户文档明确区分加入、迁入、引用快照、草稿、定稿、检索可见性和权限。
8. 发布说明标明新内容布局版本、最低兼容程序版本和回退方式。

### 20.2 发布与回退

先发布只读 locator 兼容及格式识别，再启用新建/迁移写入。工作区根 schema.json 在首次新布局写入前持久写入 layout_version 和 minimum_writer_version。受支持的启动器、升级器及具备该校验的新版本程序在启动前检查，不兼容时拒绝写模式。任意历史二进制可能根本不认识此标记，因此不能声称它会自动拒绝；部署必须停止旧进程，并通过受支持启动入口执行版本预检。手工绕过入口运行旧二进制属于不支持且可能损坏数据的操作，发布说明必须明确警告。

回退分两类：

- 只回退 UI/功能开关：存储兼容层必须保留，现有专题文档仍可读取。
- 回退到不认识新布局的旧二进制：必须先使用受支持迁移工具迁回并完成验证；不能直接替换二进制。

策略开关关闭不能把已排除对象自动恢复成全局可见。关闭检索新功能时应保留策略执行或拒绝不兼容启动。

### 20.3 未纳入第一版的明确事项

跨文档语义一致性证明、复杂依赖图、多用户权限、分布式多写者、任意目录外原地管理、自动远程同步均不在第一版范围。它们不能影响本版对已承诺的文件集中、目录追踪和检索控制的验收完整性。

## 21 源码依据

以下链接固定到本次核对的提交，避免后续 master 改变导致依据漂移。

1. [Topic、成员、Outline、请求与详情模型](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/models/models.go#L632-L775)
2. [Topic 创建、成员与编织存储](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/topicstore/store.go)
3. [Topic 事件订阅与模块接线](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/modules/topic/module.go)
4. [Thought 路径、读写与投影](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/markdown/thought.go)
5. [文档存储、revision 及锁](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentstore/store.go)
6. [文档 Git 内容路径白名单](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentstore/paths.go)
7. [文档校验、定稿及精确导出](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentflow/service.go)
8. [私有来源证据](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentflow/evidence.go)
9. [工作区路径配置](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/workspace/workspace.go)
10. [搜索数据库与 TopicID 过滤](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/searchdb/store.go)
11. [HTTP 搜索、专题接口及候选补充](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/modules/application/thoughtflow/service/service.go)
12. [Web 路由恢复、专题详情与成员展示](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/web/app.js)
13. [Profile 注册与冻结版本解析](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentprofile/registry.go)
14. [实际最终正文校验](https://github.com/muidea/thought-flow/blob/7493027803e91230e1ca0d460e0294e13a07d1e0/internal/pkg/documentvalidation/validate.go)

## 22 实施检查清单

- [ ] 先建立稳定 ID 到物理位置的统一解析，再移动任何正式数据。
- [ ] 先覆盖所有读取和扫描入口，再开放专题目录新建。
- [ ] 先实现恢复和私有白名单，再开放迁移执行。
- [ ] 先在所有召回支路加入范围控制，再向用户开放全局检索开关。
- [ ] 目录和状态由事实派生，不复制一套可独立修改的完成状态。
- [ ] 单文档最终格式和校验规则继续由各自冻结 profile 决定。
- [ ] 通过真实故障注入、重启、离线包与回归测试后，再标记首版收口。

最终交付形态是：**一个专题物理工作区，一份权威目录，多篇独立且可追踪的文档，以及明确可控的全局检索范围。**

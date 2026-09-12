## ADDED Requirements

### Requirement: Per-mount operation counters are cumulative and bounded

每个挂载 SHALL 提供累计的操作数、错误数与字节数计数，记录路径 MUST 为常数开销（不取锁、不分配、不存储单次操作），且计数占用的存储 MUST 与操作次数无关。计数 MUST NOT 因时间窗口滑动而重置，因而能与有界的事件历史并存：快照同时给出累计值与保留窗口，读者可以区分"自启动以来的总量"与"最近一段的样本"。

#### Scenario: Counters survive the event window

- **WHEN** 一个挂载完成的操作数远超其事件历史保留量
- **THEN** 累计计数反映全部操作，而事件历史只保留最近一段；两者同时在快照中可见且不互相影响

#### Scenario: Failed operations are counted separately

- **WHEN** 若干操作成功、若干操作失败
- **THEN** 累计操作数包含全部尝试，累计错误数等于失败次数，且失败操作携带的字节数不污染成功字节统计

#### Scenario: Recording does not allocate

- **WHEN** 一次读取完成并记录计数
- **THEN** 记录过程不产生堆分配，也不获取任何互斥锁

### Requirement: Latency distribution is available as a bounded histogram

每个挂载 SHALL 提供固定桶的延迟直方图，桶数与边界为编译期常量、不随操作次数增长。直方图 MUST 覆盖从毫秒级到秒级的量程并包含溢出桶，使高分位（p95/p99）可由直方图派生而无需保留单次操作的耗时。派生的均值与分位数 MUST 与直方图内容一致。

#### Scenario: Tail latency is derivable

- **WHEN** 一批操作的延迟跨越多个桶
- **THEN** 分位数由桶计数派生，且 p95 不小于 p50，p99 不小于 p95

#### Scenario: Overflow bucket keeps the distribution honest

- **WHEN** 存在超出最大桶边界的操作
- **THEN** 这些操作计入溢出桶，且分位派生不把它们丢出总量

#### Scenario: Histogram storage is constant

- **WHEN** 记录的延迟样本数增长若干个数量级
- **THEN** 直方图占用的存储不变，且快照中只有桶边界与计数

### Requirement: Counters are exposed per mount in the debug snapshot

累计计数与延迟分布 SHALL 出现在每个挂载的调试快照中，并与该挂载的事件历史来自同一次读取，使消费方无需额外聚合或多次请求即可获得吞吐与尾延迟。新增暴露 MUST 是快照的追加字段，不破坏既有消费方的解析。

#### Scenario: Snapshot carries counters per mount

- **WHEN** 消费方读取调试快照
- **THEN** 每个挂载都带有累计操作数、错误数、字节数、延迟直方图与派生的均值/分位数

#### Scenario: Zero-activity mount reports zeros without error

- **WHEN** 一个挂载自启动以来没有处理过任何操作
- **THEN** 其计数全部为零、直方图为空，且快照仍然可解析、派生分位数不产生除零错误

## ADDED Requirements

### Requirement: Wrapper results are named in the caller's vocabulary

位于后端之上的包装层（如文件名加密层）在返回条目时 MUST 使用调用方词汇中的名称（明文名），MUST NOT 把后端名（如密文名）当作条目名称返回。后端名 MUST 通过条目的附加信息单独可取得，使需要它的调用方（诊断、后端名解析）仍能拿到。该要求与写入类操作已遵循的约定一致：包装层在上传与建目录成功时映射回明文名，列表时逐个解密。

#### Scenario: Rename returns the plaintext name

- **WHEN** 调用方在加密挂载上重命名一个文件且远端成功
- **THEN** 返回条目的名称是调用方给出的明文新名，而不是后端的密文名；后端密文名可通过附加信息取得

#### Scenario: Move returns the plaintext name

- **WHEN** 调用方在加密挂载上把一个文件移动到另一个目录且远端成功
- **THEN** 返回条目保留明文名称与新的父标识，而不是后端的密文名

#### Scenario: The backend still receives the backend name

- **WHEN** 包装层执行重命名或移动
- **THEN** 传给后端的名称仍是后端词汇中的名称，包装层不得因映射回传值而改变发往后端的参数

### Requirement: View entries are named by their path

按路径写入视图的每个提交点 MUST 保证条目名称等于该路径的基名；当上游给出的名称与路径不一致时，系统 MUST 以路径为准归一化，并显式记录这次不一致（路径、给定名称与标识），使上游的契约破坏可见而不是以"用户无法寻址的假条目"形式呈现。该不变量适用于所有按路径提交的入口，包括上传完成与远端重命名完成。

#### Scenario: A mismatched name is corrected and recorded

- **WHEN** 某个提交点收到名称与其路径基名不一致的条目
- **THEN** 视图按路径基名存储该条目，且记录一条包含路径、原名称与标识的告警

#### Scenario: Consistent entries are stored unchanged

- **WHEN** 提交点收到的条目名称与路径基名一致
- **THEN** 视图原样存储该条目，不产生告警，也不改变其它字段

#### Scenario: Listings never surface an unaddressable name

- **WHEN** 一次重命名或覆盖写完成后读取该目录的列表
- **THEN** 结果中不出现以后端名（密文名）命名的条目，用户看到的名称与其可寻址路径一致

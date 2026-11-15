# Filosofia do SDK Y

## Visão

Y é um SDK para construir harnesses de agentes. O Y não escolhe um único
produto, fluxo de trabalho ou conjunto de ferramentas. Ele fornece um core
forte, pequeno e estável, e permite que cada aplicação componha o harness de
que precisa por meio de componentes plugados nesse core.

O core é **fechado no sentido arquitetural**: suas invariantes e os caminhos
pelos quais o estado e os efeitos entram no runtime são deliberados e
controlados. Ele não é uma coleção aberta de pontos de mutação. A composição
continua aberta: providers, ferramentas, memória, contexto, políticas,
transporte, interfaces, execução e integrações são componentes que usam os
contratos do core.

Esta é a visão alvo do SDK. As garantias descritas aqui orientam uma evolução
gradual e não afirmam que todas já estejam presentes na implementação atual.

```text
Harness = core Y + componentes escolhidos pela aplicação + configuração
```

Uma aplicação pode ter um agente de código, um agente de suporte, um worker
distribuído ou vários harnesses no mesmo processo. Todos podem usar o mesmo
loop, protocolo, lifecycle e contratos sem adotar os mesmos componentes.

## O que o core deve garantir

O core deve concentrar as semânticas que precisam permanecer coerentes entre
aplicações:

- ciclo de vida de runs, turns e chamadas de ferramenta;
- mensagens, eventos, snapshots, retomada e idempotência;
- composição e despacho de componentes;
- resolução de dependências e identidade de uma geração ativa;
- autorização, capabilities e pontos de aprovação;
- cancelamento, safe points e encerramento ordenado;
- contratos públicos estáveis, independentes de uma aplicação concreta.

O core decide **como** componentes são compostos e governados. Um componente
decide **o que** fornecer: um provider, uma ferramenta, uma fonte de contexto,
um backend de sessão, uma política ou uma integração com outro serviço.

Implementações concretas, armazenamento, transporte, interfaces de usuário,
modelos, ferramentas e regras de produto ficam em componentes ou adapters. O
core não deve conhecer os detalhes desses produtos para executar suas
invariantes.

## Componentes como unidade de composição

Toda capacidade que uma aplicação possa escolher, configurar, substituir ou
retirar deve ter uma fronteira de componente. Isso inclui capacidades nativas
do Y: recursos compilados podem ser incluídos no binário e ainda assim entrar
no harness por meio do mesmo modelo de composição.

Um componente declara:

- identidade e versão de seu contrato;
- serviços que fornece;
- serviços e versões que requer;
- capabilities que solicita;
- configuração e ciclo de vida;
- recursos e registros que possui.

O contrato não deve expor o estado interno do agente para mutação livre. Um
componente interage com o runtime por dependências explícitas e operações
mediadas pelo core. A configuração de uma aplicação seleciona componentes,
conecta suas dependências e define suas permissões.

Componentes devem ter granularidade de substituição útil. Um provider ou um
conjunto coeso de ferramentas pode ser um componente; cada função auxiliar não
precisa virar um componente por si só. Componentes menores fazem sentido
quando podem ser habilitados, substituídos, isolados ou autorizados de forma
independente.

## Efeitos, dependências e propriedade

O modelo do Cordis oferece ao Y duas lentes de runtime:

### Efeitos pertencem a quem os criou

Ao ativar, um componente adquire recursos e instala contribuições no harness.
Essas aquisições devem ser registradas junto ao componente: processos,
conexões, workers, ferramentas, hooks, fontes de contexto e assinaturas de
eventos. Ao desativar, o runtime cancela o trabalho em andamento, drena o que
precisa terminar e libera as aquisições na ordem inversa.

Uma ativação que falha no meio deve desfazer as aquisições já concluídas. O
encerramento deve ser idempotente e respeitar limites de tempo e cancelamento.
Um componente não deve deixar registros órfãos no registry compartilhado.

### Dependências são requisitos declarados

Um componente só ativa quando os serviços que declarou estão disponíveis e
compatíveis. O runtime acompanha a identidade do provider e sua geração, não
apenas um booleano de disponibilidade. Quando um provider muda, os
dependentes são reconciliados em uma ordem definida pelo grafo de
dependências. Dependências cíclicas são detectadas e reportadas antes de uma
ativação incompleta.

Dependência não é autorização. Um componente pode depender de um serviço e
ainda não ter permissão para executar todas as operações que esse serviço
oferece. O core resolve ambas as questões: disponibilidade e autoridade.

## O limite da reversão

Y deve ser preciso sobre o que pode desfazer. Abrir uma conexão e fechar essa
conexão é uma aquisição gerenciada. Enviar uma mensagem, modificar um arquivo
do usuário ou chamar uma API externa é uma emissão que pode produzir efeitos
observáveis fora do processo. Fechar o componente não apaga automaticamente
essas consequências.

Dados duráveis do usuário — transcript, sessões, checkpoints e memória — não
são recursos temporários de um componente. Eles têm contratos de persistência,
retenção, migração e exclusão próprios. Retirar o componente que os acessa não
deve apagar esses dados.

Cada componente de persistência escolhe explicitamente `required` ou
`degraded` em sua configuração. Em `required`, uma falha de prontidão impede a
ativação. Em `degraded`, o componente expõe seu estado e as gravações continuam
retornando erros. Essa escolha pertence à configuração do componente; o runtime
genérico continua sem política própria de armazenamento. O comportamento
`fail-open` de uma leitura é uma regra de leitura, não uma promessa de
disponibilidade nem uma política para gravações.

Quando uma ação externa admite compensação, ela deve declarar essa semântica e
seus limites. Idempotência, autorização, auditoria e aprovação continuam sendo
necessárias; uma função de cleanup não converte uma emissão em uma operação
reversível.

## Segurança e fronteiras de confiança

Capabilities expressam autoridade solicitada. O core aplica policy nos pontos
de acesso que controla. Código nativo confiável pode ter acesso ao processo e
deve ser tratado de acordo com essa confiança. Código não confiável precisa de
uma fronteira real, como WASM ou um processo isolado, com chamadas ao host
mediadas por capabilities.

O modelo de componentes melhora ownership e composição. Ele não substitui
sandboxing nem prova a segurança de código arbitrário.

## Consistência durante mudanças

Uma geração ativa de runtime reúne os componentes e bindings visíveis a uma
operação. A lista de ferramentas apresentada ao modelo e os handlers que
executam as respostas devem pertencer à mesma geração. Hooks, contexto e
serviços usados naquela operação também precisam de uma visão coerente.

Atualizações devem entrar em safe points definidos. A primeira semântica pode
aplicar mudanças entre turns: uma chamada em andamento termina sob a geração
que a iniciou; a próxima operação recebe a nova geração. No futuro, operações
longas podem adotar drenagem ou substituição coordenada explícita.

## Regras de projeto

1. O core possui invariantes e estado; componentes fornecem capacidades.
2. Toda dependência que altera comportamento é explícita, versionada e
   compatível com os limites de pacote do Go.
3. Todo recurso de runtime tem um owner e um encerramento observável.
4. Toda alteração de composição passa pelo lifecycle do core.
5. Operações compartilhadas e sensíveis à ordem declaram uma política de
   ordenação; o runtime não presume que são comutativas.
6. O core preserva dados duráveis e identifica separadamente efeitos externos.
7. Componentes nativos e componentes isolados usam o mesmo contrato de
   composição, com fronteiras de confiança diferentes.
8. Uma aplicação pode compor seu próprio harness sem alterar o core para cada
   produto novo.

## Resultado esperado

O resultado é um SDK em que a força do core permite variedade sem fragmentar
as semânticas. Uma aplicação instala os componentes de que precisa; outra pode
usar o mesmo agente com providers, políticas, ferramentas, contexto, sessão e
transporte diferentes. O core continua reconhecível, testável e compatível,
enquanto o espaço de harnesses permanece aberto.

O primeiro contrato público dessa direção está descrito em
[Y component contract](component-contract.md), com o estado atual e os limites
da composição estática. A semântica de cleanup, estado durável, emissões e
compensações está em [Effects and durable state](effects-and-durable-state.md).

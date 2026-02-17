# TODO

- [x] tidy up some methods (for example, `CanSee` in combat module could be moved into entities module, etc.)
- [x] add method to go-mclib/protocol (or go-mclib/data) TextComponent that returns it as a raw String() (just flattens all Text or Translate components), ANSII() for terminal color codes, ColorCodes() for bukkit color codes, MiniMessage() for converting to mini message format and LocaleString() which is same as String() but additionally converts all Translate components to English language (there is package that does this in go-mclib/data)
- [x] fix item sorter
  - [ ] fix bot not being able to reach containers more than 3 blocks above ground
  - [x] fix bot being stuck (softlock) when reaching a container randomly (maybe because the chest fails to open)
  - [x] support multiple items in a single container (one item per sign line)
- [x] more atomic events for all different parts of the game in their respective modules: OnPlayerRenderDistanceEnter, OnContainerOpen, OnEntitySpawn, OnEntityDamage, OnEntityDeath, etc. etc.

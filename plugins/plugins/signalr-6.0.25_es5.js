"use strict";

function _toConsumableArray(arr) { return _arrayWithoutHoles(arr) || _iterableToArray(arr) || _unsupportedIterableToArray(arr) || _nonIterableSpread(); }
function _nonIterableSpread() { throw new TypeError("Invalid attempt to spread non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method."); }
function _iterableToArray(iter) { if (typeof Symbol !== "undefined" && iter[Symbol.iterator] != null || iter["@@iterator"] != null) return Array.from(iter); }
function _arrayWithoutHoles(arr) { if (Array.isArray(arr)) return _arrayLikeToArray(arr); }
function _slicedToArray(arr, i) { return _arrayWithHoles(arr) || _iterableToArrayLimit(arr, i) || _unsupportedIterableToArray(arr, i) || _nonIterableRest(); }
function _nonIterableRest() { throw new TypeError("Invalid attempt to destructure non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method."); }
function _iterableToArrayLimit(arr, i) { var _i = null == arr ? null : "undefined" != typeof Symbol && arr[Symbol.iterator] || arr["@@iterator"]; if (null != _i) { var _s, _e, _x, _r, _arr = [], _n = !0, _d = !1; try { if (_x = (_i = _i.call(arr)).next, 0 === i) { if (Object(_i) !== _i) return; _n = !1; } else for (; !(_n = (_s = _x.call(_i)).done) && (_arr.push(_s.value), _arr.length !== i); _n = !0); } catch (err) { _d = !0, _e = err; } finally { try { if (!_n && null != _i["return"] && (_r = _i["return"](), Object(_r) !== _r)) return; } finally { if (_d) throw _e; } } return _arr; } }
function _arrayWithHoles(arr) { if (Array.isArray(arr)) return arr; }
function _createForOfIteratorHelper(o, allowArrayLike) { var it = typeof Symbol !== "undefined" && o[Symbol.iterator] || o["@@iterator"]; if (!it) { if (Array.isArray(o) || (it = _unsupportedIterableToArray(o)) || allowArrayLike && o && typeof o.length === "number") { if (it) o = it; var i = 0; var F = function F() {}; return { s: F, n: function n() { if (i >= o.length) return { done: true }; return { done: false, value: o[i++] }; }, e: function e(_e2) { throw _e2; }, f: F }; } throw new TypeError("Invalid attempt to iterate non-iterable instance.\nIn order to be iterable, non-array objects must have a [Symbol.iterator]() method."); } var normalCompletion = true, didErr = false, err; return { s: function s() { it = it.call(o); }, n: function n() { var step = it.next(); normalCompletion = step.done; return step; }, e: function e(_e3) { didErr = true; err = _e3; }, f: function f() { try { if (!normalCompletion && it["return"] != null) it["return"](); } finally { if (didErr) throw err; } } }; }
function _unsupportedIterableToArray(o, minLen) { if (!o) return; if (typeof o === "string") return _arrayLikeToArray(o, minLen); var n = Object.prototype.toString.call(o).slice(8, -1); if (n === "Object" && o.constructor) n = o.constructor.name; if (n === "Map" || n === "Set") return Array.from(o); if (n === "Arguments" || /^(?:Ui|I)nt(?:8|16|32)(?:Clamped)?Array$/.test(n)) return _arrayLikeToArray(o, minLen); }
function _arrayLikeToArray(arr, len) { if (len == null || len > arr.length) len = arr.length; for (var i = 0, arr2 = new Array(len); i < len; i++) arr2[i] = arr[i]; return arr2; }
function _regeneratorRuntime() { "use strict"; /*! regenerator-runtime -- Copyright (c) 2014-present, Facebook, Inc. -- license (MIT): https://github.com/facebook/regenerator/blob/main/LICENSE */ _regeneratorRuntime = function _regeneratorRuntime() { return exports; }; var exports = {}, Op = Object.prototype, hasOwn = Op.hasOwnProperty, defineProperty = Object.defineProperty || function (obj, key, desc) { obj[key] = desc.value; }, $Symbol = "function" == typeof Symbol ? Symbol : {}, iteratorSymbol = $Symbol.iterator || "@@iterator", asyncIteratorSymbol = $Symbol.asyncIterator || "@@asyncIterator", toStringTagSymbol = $Symbol.toStringTag || "@@toStringTag"; function define(obj, key, value) { return Object.defineProperty(obj, key, { value: value, enumerable: !0, configurable: !0, writable: !0 }), obj[key]; } try { define({}, ""); } catch (err) { define = function define(obj, key, value) { return obj[key] = value; }; } function wrap(innerFn, outerFn, self, tryLocsList) { var protoGenerator = outerFn && outerFn.prototype instanceof Generator ? outerFn : Generator, generator = Object.create(protoGenerator.prototype), context = new Context(tryLocsList || []); return defineProperty(generator, "_invoke", { value: makeInvokeMethod(innerFn, self, context) }), generator; } function tryCatch(fn, obj, arg) { try { return { type: "normal", arg: fn.call(obj, arg) }; } catch (err) { return { type: "throw", arg: err }; } } exports.wrap = wrap; var ContinueSentinel = {}; function Generator() {} function GeneratorFunction() {} function GeneratorFunctionPrototype() {} var IteratorPrototype = {}; define(IteratorPrototype, iteratorSymbol, function () { return this; }); var getProto = Object.getPrototypeOf, NativeIteratorPrototype = getProto && getProto(getProto(values([]))); NativeIteratorPrototype && NativeIteratorPrototype !== Op && hasOwn.call(NativeIteratorPrototype, iteratorSymbol) && (IteratorPrototype = NativeIteratorPrototype); var Gp = GeneratorFunctionPrototype.prototype = Generator.prototype = Object.create(IteratorPrototype); function defineIteratorMethods(prototype) { ["next", "throw", "return"].forEach(function (method) { define(prototype, method, function (arg) { return this._invoke(method, arg); }); }); } function AsyncIterator(generator, PromiseImpl) { function invoke(method, arg, resolve, reject) { var record = tryCatch(generator[method], generator, arg); if ("throw" !== record.type) { var result = record.arg, value = result.value; return value && "object" == _typeof(value) && hasOwn.call(value, "__await") ? PromiseImpl.resolve(value.__await).then(function (value) { invoke("next", value, resolve, reject); }, function (err) { invoke("throw", err, resolve, reject); }) : PromiseImpl.resolve(value).then(function (unwrapped) { result.value = unwrapped, resolve(result); }, function (error) { return invoke("throw", error, resolve, reject); }); } reject(record.arg); } var previousPromise; defineProperty(this, "_invoke", { value: function value(method, arg) { function callInvokeWithMethodAndArg() { return new PromiseImpl(function (resolve, reject) { invoke(method, arg, resolve, reject); }); } return previousPromise = previousPromise ? previousPromise.then(callInvokeWithMethodAndArg, callInvokeWithMethodAndArg) : callInvokeWithMethodAndArg(); } }); } function makeInvokeMethod(innerFn, self, context) { var state = "suspendedStart"; return function (method, arg) { if ("executing" === state) throw new Error("Generator is already running"); if ("completed" === state) { if ("throw" === method) throw arg; return doneResult(); } for (context.method = method, context.arg = arg;;) { var delegate = context.delegate; if (delegate) { var delegateResult = maybeInvokeDelegate(delegate, context); if (delegateResult) { if (delegateResult === ContinueSentinel) continue; return delegateResult; } } if ("next" === context.method) context.sent = context._sent = context.arg;else if ("throw" === context.method) { if ("suspendedStart" === state) throw state = "completed", context.arg; context.dispatchException(context.arg); } else "return" === context.method && context.abrupt("return", context.arg); state = "executing"; var record = tryCatch(innerFn, self, context); if ("normal" === record.type) { if (state = context.done ? "completed" : "suspendedYield", record.arg === ContinueSentinel) continue; return { value: record.arg, done: context.done }; } "throw" === record.type && (state = "completed", context.method = "throw", context.arg = record.arg); } }; } function maybeInvokeDelegate(delegate, context) { var methodName = context.method, method = delegate.iterator[methodName]; if (undefined === method) return context.delegate = null, "throw" === methodName && delegate.iterator["return"] && (context.method = "return", context.arg = undefined, maybeInvokeDelegate(delegate, context), "throw" === context.method) || "return" !== methodName && (context.method = "throw", context.arg = new TypeError("The iterator does not provide a '" + methodName + "' method")), ContinueSentinel; var record = tryCatch(method, delegate.iterator, context.arg); if ("throw" === record.type) return context.method = "throw", context.arg = record.arg, context.delegate = null, ContinueSentinel; var info = record.arg; return info ? info.done ? (context[delegate.resultName] = info.value, context.next = delegate.nextLoc, "return" !== context.method && (context.method = "next", context.arg = undefined), context.delegate = null, ContinueSentinel) : info : (context.method = "throw", context.arg = new TypeError("iterator result is not an object"), context.delegate = null, ContinueSentinel); } function pushTryEntry(locs) { var entry = { tryLoc: locs[0] }; 1 in locs && (entry.catchLoc = locs[1]), 2 in locs && (entry.finallyLoc = locs[2], entry.afterLoc = locs[3]), this.tryEntries.push(entry); } function resetTryEntry(entry) { var record = entry.completion || {}; record.type = "normal", delete record.arg, entry.completion = record; } function Context(tryLocsList) { this.tryEntries = [{ tryLoc: "root" }], tryLocsList.forEach(pushTryEntry, this), this.reset(!0); } function values(iterable) { if (iterable) { var iteratorMethod = iterable[iteratorSymbol]; if (iteratorMethod) return iteratorMethod.call(iterable); if ("function" == typeof iterable.next) return iterable; if (!isNaN(iterable.length)) { var i = -1, next = function next() { for (; ++i < iterable.length;) if (hasOwn.call(iterable, i)) return next.value = iterable[i], next.done = !1, next; return next.value = undefined, next.done = !0, next; }; return next.next = next; } } return { next: doneResult }; } function doneResult() { return { value: undefined, done: !0 }; } return GeneratorFunction.prototype = GeneratorFunctionPrototype, defineProperty(Gp, "constructor", { value: GeneratorFunctionPrototype, configurable: !0 }), defineProperty(GeneratorFunctionPrototype, "constructor", { value: GeneratorFunction, configurable: !0 }), GeneratorFunction.displayName = define(GeneratorFunctionPrototype, toStringTagSymbol, "GeneratorFunction"), exports.isGeneratorFunction = function (genFun) { var ctor = "function" == typeof genFun && genFun.constructor; return !!ctor && (ctor === GeneratorFunction || "GeneratorFunction" === (ctor.displayName || ctor.name)); }, exports.mark = function (genFun) { return Object.setPrototypeOf ? Object.setPrototypeOf(genFun, GeneratorFunctionPrototype) : (genFun.__proto__ = GeneratorFunctionPrototype, define(genFun, toStringTagSymbol, "GeneratorFunction")), genFun.prototype = Object.create(Gp), genFun; }, exports.awrap = function (arg) { return { __await: arg }; }, defineIteratorMethods(AsyncIterator.prototype), define(AsyncIterator.prototype, asyncIteratorSymbol, function () { return this; }), exports.AsyncIterator = AsyncIterator, exports.async = function (innerFn, outerFn, self, tryLocsList, PromiseImpl) { void 0 === PromiseImpl && (PromiseImpl = Promise); var iter = new AsyncIterator(wrap(innerFn, outerFn, self, tryLocsList), PromiseImpl); return exports.isGeneratorFunction(outerFn) ? iter : iter.next().then(function (result) { return result.done ? result.value : iter.next(); }); }, defineIteratorMethods(Gp), define(Gp, toStringTagSymbol, "Generator"), define(Gp, iteratorSymbol, function () { return this; }), define(Gp, "toString", function () { return "[object Generator]"; }), exports.keys = function (val) { var object = Object(val), keys = []; for (var key in object) keys.push(key); return keys.reverse(), function next() { for (; keys.length;) { var key = keys.pop(); if (key in object) return next.value = key, next.done = !1, next; } return next.done = !0, next; }; }, exports.values = values, Context.prototype = { constructor: Context, reset: function reset(skipTempReset) { if (this.prev = 0, this.next = 0, this.sent = this._sent = undefined, this.done = !1, this.delegate = null, this.method = "next", this.arg = undefined, this.tryEntries.forEach(resetTryEntry), !skipTempReset) for (var name in this) "t" === name.charAt(0) && hasOwn.call(this, name) && !isNaN(+name.slice(1)) && (this[name] = undefined); }, stop: function stop() { this.done = !0; var rootRecord = this.tryEntries[0].completion; if ("throw" === rootRecord.type) throw rootRecord.arg; return this.rval; }, dispatchException: function dispatchException(exception) { if (this.done) throw exception; var context = this; function handle(loc, caught) { return record.type = "throw", record.arg = exception, context.next = loc, caught && (context.method = "next", context.arg = undefined), !!caught; } for (var i = this.tryEntries.length - 1; i >= 0; --i) { var entry = this.tryEntries[i], record = entry.completion; if ("root" === entry.tryLoc) return handle("end"); if (entry.tryLoc <= this.prev) { var hasCatch = hasOwn.call(entry, "catchLoc"), hasFinally = hasOwn.call(entry, "finallyLoc"); if (hasCatch && hasFinally) { if (this.prev < entry.catchLoc) return handle(entry.catchLoc, !0); if (this.prev < entry.finallyLoc) return handle(entry.finallyLoc); } else if (hasCatch) { if (this.prev < entry.catchLoc) return handle(entry.catchLoc, !0); } else { if (!hasFinally) throw new Error("try statement without catch or finally"); if (this.prev < entry.finallyLoc) return handle(entry.finallyLoc); } } } }, abrupt: function abrupt(type, arg) { for (var i = this.tryEntries.length - 1; i >= 0; --i) { var entry = this.tryEntries[i]; if (entry.tryLoc <= this.prev && hasOwn.call(entry, "finallyLoc") && this.prev < entry.finallyLoc) { var finallyEntry = entry; break; } } finallyEntry && ("break" === type || "continue" === type) && finallyEntry.tryLoc <= arg && arg <= finallyEntry.finallyLoc && (finallyEntry = null); var record = finallyEntry ? finallyEntry.completion : {}; return record.type = type, record.arg = arg, finallyEntry ? (this.method = "next", this.next = finallyEntry.finallyLoc, ContinueSentinel) : this.complete(record); }, complete: function complete(record, afterLoc) { if ("throw" === record.type) throw record.arg; return "break" === record.type || "continue" === record.type ? this.next = record.arg : "return" === record.type ? (this.rval = this.arg = record.arg, this.method = "return", this.next = "end") : "normal" === record.type && afterLoc && (this.next = afterLoc), ContinueSentinel; }, finish: function finish(finallyLoc) { for (var i = this.tryEntries.length - 1; i >= 0; --i) { var entry = this.tryEntries[i]; if (entry.finallyLoc === finallyLoc) return this.complete(entry.completion, entry.afterLoc), resetTryEntry(entry), ContinueSentinel; } }, "catch": function _catch(tryLoc) { for (var i = this.tryEntries.length - 1; i >= 0; --i) { var entry = this.tryEntries[i]; if (entry.tryLoc === tryLoc) { var record = entry.completion; if ("throw" === record.type) { var thrown = record.arg; resetTryEntry(entry); } return thrown; } } throw new Error("illegal catch attempt"); }, delegateYield: function delegateYield(iterable, resultName, nextLoc) { return this.delegate = { iterator: values(iterable), resultName: resultName, nextLoc: nextLoc }, "next" === this.method && (this.arg = undefined), ContinueSentinel; } }, exports; }
function asyncGeneratorStep(gen, resolve, reject, _next, _throw, key, arg) { try { var info = gen[key](arg); var value = info.value; } catch (error) { reject(error); return; } if (info.done) { resolve(value); } else { Promise.resolve(value).then(_next, _throw); } }
function _asyncToGenerator(fn) { return function () { var self = this, args = arguments; return new Promise(function (resolve, reject) { var gen = fn.apply(self, args); function _next(value) { asyncGeneratorStep(gen, resolve, reject, _next, _throw, "next", value); } function _throw(err) { asyncGeneratorStep(gen, resolve, reject, _next, _throw, "throw", err); } _next(undefined); }); }; }
function ownKeys(object, enumerableOnly) { var keys = Object.keys(object); if (Object.getOwnPropertySymbols) { var symbols = Object.getOwnPropertySymbols(object); enumerableOnly && (symbols = symbols.filter(function (sym) { return Object.getOwnPropertyDescriptor(object, sym).enumerable; })), keys.push.apply(keys, symbols); } return keys; }
function _objectSpread(target) { for (var i = 1; i < arguments.length; i++) { var source = null != arguments[i] ? arguments[i] : {}; i % 2 ? ownKeys(Object(source), !0).forEach(function (key) { _defineProperty(target, key, source[key]); }) : Object.getOwnPropertyDescriptors ? Object.defineProperties(target, Object.getOwnPropertyDescriptors(source)) : ownKeys(Object(source)).forEach(function (key) { Object.defineProperty(target, key, Object.getOwnPropertyDescriptor(source, key)); }); } return target; }
function _defineProperty(obj, key, value) { key = _toPropertyKey(key); if (key in obj) { Object.defineProperty(obj, key, { value: value, enumerable: true, configurable: true, writable: true }); } else { obj[key] = value; } return obj; }
function _defineProperties(target, props) { for (var i = 0; i < props.length; i++) { var descriptor = props[i]; descriptor.enumerable = descriptor.enumerable || false; descriptor.configurable = true; if ("value" in descriptor) descriptor.writable = true; Object.defineProperty(target, _toPropertyKey(descriptor.key), descriptor); } }
function _createClass(Constructor, protoProps, staticProps) { if (protoProps) _defineProperties(Constructor.prototype, protoProps); if (staticProps) _defineProperties(Constructor, staticProps); Object.defineProperty(Constructor, "prototype", { writable: false }); return Constructor; }
function _toPropertyKey(arg) { var key = _toPrimitive(arg, "string"); return _typeof(key) === "symbol" ? key : String(key); }
function _toPrimitive(input, hint) { if (_typeof(input) !== "object" || input === null) return input; var prim = input[Symbol.toPrimitive]; if (prim !== undefined) { var res = prim.call(input, hint || "default"); if (_typeof(res) !== "object") return res; throw new TypeError("@@toPrimitive must return a primitive value."); } return (hint === "string" ? String : Number)(input); }
function _classCallCheck(instance, Constructor) { if (!(instance instanceof Constructor)) { throw new TypeError("Cannot call a class as a function"); } }
function _inherits(subClass, superClass) { if (typeof superClass !== "function" && superClass !== null) { throw new TypeError("Super expression must either be null or a function"); } subClass.prototype = Object.create(superClass && superClass.prototype, { constructor: { value: subClass, writable: true, configurable: true } }); Object.defineProperty(subClass, "prototype", { writable: false }); if (superClass) _setPrototypeOf(subClass, superClass); }
function _createSuper(Derived) { var hasNativeReflectConstruct = _isNativeReflectConstruct(); return function _createSuperInternal() { var Super = _getPrototypeOf(Derived), result; if (hasNativeReflectConstruct) { var NewTarget = _getPrototypeOf(this).constructor; result = Reflect.construct(Super, arguments, NewTarget); } else { result = Super.apply(this, arguments); } return _possibleConstructorReturn(this, result); }; }
function _possibleConstructorReturn(self, call) { if (call && (_typeof(call) === "object" || typeof call === "function")) { return call; } else if (call !== void 0) { throw new TypeError("Derived constructors may only return object or undefined"); } return _assertThisInitialized(self); }
function _assertThisInitialized(self) { if (self === void 0) { throw new ReferenceError("this hasn't been initialised - super() hasn't been called"); } return self; }
function _wrapNativeSuper(Class) { var _cache = typeof Map === "function" ? new Map() : undefined; _wrapNativeSuper = function _wrapNativeSuper(Class) { if (Class === null || !_isNativeFunction(Class)) return Class; if (typeof Class !== "function") { throw new TypeError("Super expression must either be null or a function"); } if (typeof _cache !== "undefined") { if (_cache.has(Class)) return _cache.get(Class); _cache.set(Class, Wrapper); } function Wrapper() { return _construct(Class, arguments, _getPrototypeOf(this).constructor); } Wrapper.prototype = Object.create(Class.prototype, { constructor: { value: Wrapper, enumerable: false, writable: true, configurable: true } }); return _setPrototypeOf(Wrapper, Class); }; return _wrapNativeSuper(Class); }
function _construct(Parent, args, Class) { if (_isNativeReflectConstruct()) { _construct = Reflect.construct.bind(); } else { _construct = function _construct(Parent, args, Class) { var a = [null]; a.push.apply(a, args); var Constructor = Function.bind.apply(Parent, a); var instance = new Constructor(); if (Class) _setPrototypeOf(instance, Class.prototype); return instance; }; } return _construct.apply(null, arguments); }
function _isNativeReflectConstruct() { if (typeof Reflect === "undefined" || !Reflect.construct) return false; if (Reflect.construct.sham) return false; if (typeof Proxy === "function") return true; try { Boolean.prototype.valueOf.call(Reflect.construct(Boolean, [], function () {})); return true; } catch (e) { return false; } }
function _isNativeFunction(fn) { return Function.toString.call(fn).indexOf("[native code]") !== -1; }
function _setPrototypeOf(o, p) { _setPrototypeOf = Object.setPrototypeOf ? Object.setPrototypeOf.bind() : function _setPrototypeOf(o, p) { o.__proto__ = p; return o; }; return _setPrototypeOf(o, p); }
function _getPrototypeOf(o) { _getPrototypeOf = Object.setPrototypeOf ? Object.getPrototypeOf.bind() : function _getPrototypeOf(o) { return o.__proto__ || Object.getPrototypeOf(o); }; return _getPrototypeOf(o); }
function _typeof(obj) { "@babel/helpers - typeof"; return _typeof = "function" == typeof Symbol && "symbol" == typeof Symbol.iterator ? function (obj) { return typeof obj; } : function (obj) { return obj && "function" == typeof Symbol && obj.constructor === Symbol && obj !== Symbol.prototype ? "symbol" : typeof obj; }, _typeof(obj); }
(function webpackUniversalModuleDefinition(root, factory) {
  if ((typeof exports === "undefined" ? "undefined" : _typeof(exports)) === 'object' && (typeof module === "undefined" ? "undefined" : _typeof(module)) === 'object') module.exports = factory();else if (typeof define === 'function' && define.amd) define([], factory);else if ((typeof exports === "undefined" ? "undefined" : _typeof(exports)) === 'object') exports["signalR"] = factory();else root["signalR"] = factory();
})(self, function () {
  return (/******/function () {
      // webpackBootstrap
      /******/
      "use strict";

      /******/ // The require scope
      /******/
      var __webpack_require__ = {};
      /******/
      /************************************************************************/
      /******/ /* webpack/runtime/define property getters */
      /******/
      (function () {
        /******/ // define getter functions for harmony exports
        /******/__webpack_require__.d = function (exports, definition) {
          /******/for (var key in definition) {
            /******/if (__webpack_require__.o(definition, key) && !__webpack_require__.o(exports, key)) {
              /******/Object.defineProperty(exports, key, {
                enumerable: true,
                get: definition[key]
              });
              /******/
            }
            /******/
          }
          /******/
        };
        /******/
      })();
      /******/
      /******/ /* webpack/runtime/global */
      /******/
      (function () {
        /******/__webpack_require__.g = function () {
          /******/if ((typeof globalThis === "undefined" ? "undefined" : _typeof(globalThis)) === 'object') return globalThis;
          /******/
          try {
            /******/return this || new Function('return this')();
            /******/
          } catch (e) {
            /******/if ((typeof window === "undefined" ? "undefined" : _typeof(window)) === 'object') return window;
            /******/
          }
          /******/
        }();
        /******/
      })();
      /******/
      /******/ /* webpack/runtime/hasOwnProperty shorthand */
      /******/
      (function () {
        /******/__webpack_require__.o = function (obj, prop) {
          return Object.prototype.hasOwnProperty.call(obj, prop);
        };
        /******/
      })();
      /******/
      /******/ /* webpack/runtime/make namespace object */
      /******/
      (function () {
        /******/ // define __esModule on exports
        /******/__webpack_require__.r = function (exports) {
          /******/if (typeof Symbol !== 'undefined' && Symbol.toStringTag) {
            /******/Object.defineProperty(exports, Symbol.toStringTag, {
              value: 'Module'
            });
            /******/
          }
          /******/
          Object.defineProperty(exports, '__esModule', {
            value: true
          });
          /******/
        };
        /******/
      })();
      /******/
      /************************************************************************/
      var __webpack_exports__ = {};
      // ESM COMPAT FLAG
      __webpack_require__.r(__webpack_exports__);

      // EXPORTS
      __webpack_require__.d(__webpack_exports__, {
        "AbortError": function AbortError() {
          return (/* reexport */_AbortError
          );
        },
        "DefaultHttpClient": function DefaultHttpClient() {
          return (/* reexport */_DefaultHttpClient
          );
        },
        "HttpClient": function HttpClient() {
          return (/* reexport */_HttpClient
          );
        },
        "HttpError": function HttpError() {
          return (/* reexport */_HttpError
          );
        },
        "HttpResponse": function HttpResponse() {
          return (/* reexport */_HttpResponse
          );
        },
        "HttpTransportType": function HttpTransportType() {
          return (/* reexport */_HttpTransportType
          );
        },
        "HubConnection": function HubConnection() {
          return (/* reexport */_HubConnection
          );
        },
        "HubConnectionBuilder": function HubConnectionBuilder() {
          return (/* reexport */_HubConnectionBuilder
          );
        },
        "HubConnectionState": function HubConnectionState() {
          return (/* reexport */_HubConnectionState
          );
        },
        "JsonHubProtocol": function JsonHubProtocol() {
          return (/* reexport */_JsonHubProtocol
          );
        },
        "LogLevel": function LogLevel() {
          return (/* reexport */_LogLevel
          );
        },
        "MessageType": function MessageType() {
          return (/* reexport */_MessageType
          );
        },
        "NullLogger": function NullLogger() {
          return (/* reexport */_NullLogger
          );
        },
        "Subject": function Subject() {
          return (/* reexport */_Subject
          );
        },
        "TimeoutError": function TimeoutError() {
          return (/* reexport */_TimeoutError
          );
        },
        "TransferFormat": function TransferFormat() {
          return (/* reexport */_TransferFormat
          );
        },
        "VERSION": function VERSION() {
          return (/* reexport */_VERSION
          );
        }
      });
      ; // CONCATENATED MODULE: ./src/Errors.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      /** Error thrown when an HTTP request fails. */
      var _HttpError = /*#__PURE__*/function (_Error) {
        _inherits(_HttpError, _Error);
        var _super = _createSuper(_HttpError);
        /** Constructs a new instance of {@link @microsoft/signalr.HttpError}.
         *
         * @param {string} errorMessage A descriptive error message.
         * @param {number} statusCode The HTTP status code represented by this error.
         */
        function _HttpError(errorMessage, statusCode) {
          var _this;
          _classCallCheck(this, _HttpError);
          var trueProto = (this instanceof _HttpError ? this.constructor : void 0).prototype;
          _this = _super.call(this, "".concat(errorMessage, ": Status code '").concat(statusCode, "'"));
          _this.statusCode = statusCode;
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this.__proto__ = trueProto;
          return _this;
        }
        return _createClass(_HttpError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when a timeout elapses. */
      var _TimeoutError = /*#__PURE__*/function (_Error2) {
        _inherits(_TimeoutError, _Error2);
        var _super2 = _createSuper(_TimeoutError);
        /** Constructs a new instance of {@link @microsoft/signalr.TimeoutError}.
         *
         * @param {string} errorMessage A descriptive error message.
         */
        function _TimeoutError() {
          var _this2;
          var errorMessage = arguments.length > 0 && arguments[0] !== undefined ? arguments[0] : "A timeout occurred.";
          _classCallCheck(this, _TimeoutError);
          var trueProto = (this instanceof _TimeoutError ? this.constructor : void 0).prototype;
          _this2 = _super2.call(this, errorMessage);
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this2.__proto__ = trueProto;
          return _this2;
        }
        return _createClass(_TimeoutError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when an action is aborted. */
      var _AbortError = /*#__PURE__*/function (_Error3) {
        _inherits(_AbortError, _Error3);
        var _super3 = _createSuper(_AbortError);
        /** Constructs a new instance of {@link AbortError}.
         *
         * @param {string} errorMessage A descriptive error message.
         */
        function _AbortError() {
          var _this3;
          var errorMessage = arguments.length > 0 && arguments[0] !== undefined ? arguments[0] : "An abort occurred.";
          _classCallCheck(this, _AbortError);
          var trueProto = (this instanceof _AbortError ? this.constructor : void 0).prototype;
          _this3 = _super3.call(this, errorMessage);
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this3.__proto__ = trueProto;
          return _this3;
        }
        return _createClass(_AbortError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when the selected transport is unsupported by the browser. */
      /** @private */
      var UnsupportedTransportError = /*#__PURE__*/function (_Error4) {
        _inherits(UnsupportedTransportError, _Error4);
        var _super4 = _createSuper(UnsupportedTransportError);
        /** Constructs a new instance of {@link @microsoft/signalr.UnsupportedTransportError}.
         *
         * @param {string} message A descriptive error message.
         * @param {HttpTransportType} transport The {@link @microsoft/signalr.HttpTransportType} this error occured on.
         */
        function UnsupportedTransportError(message, transport) {
          var _this4;
          _classCallCheck(this, UnsupportedTransportError);
          var trueProto = (this instanceof UnsupportedTransportError ? this.constructor : void 0).prototype;
          _this4 = _super4.call(this, message);
          _this4.transport = transport;
          _this4.errorType = 'UnsupportedTransportError';
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this4.__proto__ = trueProto;
          return _this4;
        }
        return _createClass(UnsupportedTransportError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when the selected transport is disabled by the browser. */
      /** @private */
      var DisabledTransportError = /*#__PURE__*/function (_Error5) {
        _inherits(DisabledTransportError, _Error5);
        var _super5 = _createSuper(DisabledTransportError);
        /** Constructs a new instance of {@link @microsoft/signalr.DisabledTransportError}.
         *
         * @param {string} message A descriptive error message.
         * @param {HttpTransportType} transport The {@link @microsoft/signalr.HttpTransportType} this error occured on.
         */
        function DisabledTransportError(message, transport) {
          var _this5;
          _classCallCheck(this, DisabledTransportError);
          var trueProto = (this instanceof DisabledTransportError ? this.constructor : void 0).prototype;
          _this5 = _super5.call(this, message);
          _this5.transport = transport;
          _this5.errorType = 'DisabledTransportError';
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this5.__proto__ = trueProto;
          return _this5;
        }
        return _createClass(DisabledTransportError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when the selected transport cannot be started. */
      /** @private */
      var FailedToStartTransportError = /*#__PURE__*/function (_Error6) {
        _inherits(FailedToStartTransportError, _Error6);
        var _super6 = _createSuper(FailedToStartTransportError);
        /** Constructs a new instance of {@link @microsoft/signalr.FailedToStartTransportError}.
         *
         * @param {string} message A descriptive error message.
         * @param {HttpTransportType} transport The {@link @microsoft/signalr.HttpTransportType} this error occured on.
         */
        function FailedToStartTransportError(message, transport) {
          var _this6;
          _classCallCheck(this, FailedToStartTransportError);
          var trueProto = (this instanceof FailedToStartTransportError ? this.constructor : void 0).prototype;
          _this6 = _super6.call(this, message);
          _this6.transport = transport;
          _this6.errorType = 'FailedToStartTransportError';
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this6.__proto__ = trueProto;
          return _this6;
        }
        return _createClass(FailedToStartTransportError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when the negotiation with the server failed to complete. */
      /** @private */
      var FailedToNegotiateWithServerError = /*#__PURE__*/function (_Error7) {
        _inherits(FailedToNegotiateWithServerError, _Error7);
        var _super7 = _createSuper(FailedToNegotiateWithServerError);
        /** Constructs a new instance of {@link @microsoft/signalr.FailedToNegotiateWithServerError}.
         *
         * @param {string} message A descriptive error message.
         */
        function FailedToNegotiateWithServerError(message) {
          var _this7;
          _classCallCheck(this, FailedToNegotiateWithServerError);
          var trueProto = (this instanceof FailedToNegotiateWithServerError ? this.constructor : void 0).prototype;
          _this7 = _super7.call(this, message);
          _this7.errorType = 'FailedToNegotiateWithServerError';
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this7.__proto__ = trueProto;
          return _this7;
        }
        return _createClass(FailedToNegotiateWithServerError);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      /** Error thrown when multiple errors have occured. */
      /** @private */
      var AggregateErrors = /*#__PURE__*/function (_Error8) {
        _inherits(AggregateErrors, _Error8);
        var _super8 = _createSuper(AggregateErrors);
        /** Constructs a new instance of {@link @microsoft/signalr.AggregateErrors}.
         *
         * @param {string} message A descriptive error message.
         * @param {Error[]} innerErrors The collection of errors this error is aggregating.
         */
        function AggregateErrors(message, innerErrors) {
          var _this8;
          _classCallCheck(this, AggregateErrors);
          var trueProto = (this instanceof AggregateErrors ? this.constructor : void 0).prototype;
          _this8 = _super8.call(this, message);
          _this8.innerErrors = innerErrors;
          // Workaround issue in Typescript compiler
          // https://github.com/Microsoft/TypeScript/issues/13965#issuecomment-278570200
          _this8.__proto__ = trueProto;
          return _this8;
        }
        return _createClass(AggregateErrors);
      }( /*#__PURE__*/_wrapNativeSuper(Error));
      ; // CONCATENATED MODULE: ./src/HttpClient.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      /** Represents an HTTP response. */
      var _HttpResponse = /*#__PURE__*/_createClass(function _HttpResponse(statusCode, statusText, content) {
        _classCallCheck(this, _HttpResponse);
        this.statusCode = statusCode;
        this.statusText = statusText;
        this.content = content;
      });
      /** Abstraction over an HTTP client.
       *
       * This class provides an abstraction over an HTTP client so that a different implementation can be provided on different platforms.
       */
      var _HttpClient = /*#__PURE__*/function () {
        function _HttpClient() {
          _classCallCheck(this, _HttpClient);
        }
        _createClass(_HttpClient, [{
          key: "get",
          value: function get(url, options) {
            return this.send(_objectSpread(_objectSpread({}, options), {}, {
              method: "GET",
              url: url
            }));
          }
        }, {
          key: "post",
          value: function post(url, options) {
            return this.send(_objectSpread(_objectSpread({}, options), {}, {
              method: "POST",
              url: url
            }));
          }
        }, {
          key: "delete",
          value: function _delete(url, options) {
            return this.send(_objectSpread(_objectSpread({}, options), {}, {
              method: "DELETE",
              url: url
            }));
          }
          /** Gets all cookies that apply to the specified URL.
           *
           * @param url The URL that the cookies are valid for.
           * @returns {string} A string containing all the key-value cookie pairs for the specified URL.
           */
          // @ts-ignore
        }, {
          key: "getCookieString",
          value: function getCookieString(url) {
            return "";
          }
        }]);
        return _HttpClient;
      }();
      ; // CONCATENATED MODULE: ./src/ILogger.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // These values are designed to match the ASP.NET Log Levels since that's the pattern we're emulating here.
      /** Indicates the severity of a log message.
       *
       * Log Levels are ordered in increasing severity. So `Debug` is more severe than `Trace`, etc.
       */
      var _LogLevel;
      (function (LogLevel) {
        /** Log level for very low severity diagnostic messages. */
        LogLevel[LogLevel["Trace"] = 0] = "Trace";
        /** Log level for low severity diagnostic messages. */
        LogLevel[LogLevel["Debug"] = 1] = "Debug";
        /** Log level for informational diagnostic messages. */
        LogLevel[LogLevel["Information"] = 2] = "Information";
        /** Log level for diagnostic messages that indicate a non-fatal problem. */
        LogLevel[LogLevel["Warning"] = 3] = "Warning";
        /** Log level for diagnostic messages that indicate a failure in the current operation. */
        LogLevel[LogLevel["Error"] = 4] = "Error";
        /** Log level for diagnostic messages that indicate a failure that will terminate the entire application. */
        LogLevel[LogLevel["Critical"] = 5] = "Critical";
        /** The highest possible log level. Used when configuring logging to indicate that no log messages should be emitted. */
        LogLevel[LogLevel["None"] = 6] = "None";
      })(_LogLevel || (_LogLevel = {}));
      ; // CONCATENATED MODULE: ./src/Loggers.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      /** A logger that does nothing when log messages are sent to it. */
      var _NullLogger = /*#__PURE__*/function () {
        function _NullLogger() {
          _classCallCheck(this, _NullLogger);
        }
        /** @inheritDoc */
        // eslint-disable-next-line
        _createClass(_NullLogger, [{
          key: "log",
          value: function log(_logLevel, _message) {}
        }]);
        return _NullLogger;
      }();
      /** The singleton instance of the {@link @microsoft/signalr.NullLogger}. */
      _NullLogger.instance = new _NullLogger();
      ; // CONCATENATED MODULE: ./src/Utils.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      // Version token that will be replaced by the prepack command
      /** The version of the SignalR client. */
      var _VERSION = "6.0.25";
      /** @private */
      var Arg = /*#__PURE__*/function () {
        function Arg() {
          _classCallCheck(this, Arg);
        }
        _createClass(Arg, null, [{
          key: "isRequired",
          value: function isRequired(val, name) {
            if (val === null || val === undefined) {
              throw new Error("The '".concat(name, "' argument is required."));
            }
          }
        }, {
          key: "isNotEmpty",
          value: function isNotEmpty(val, name) {
            if (!val || val.match(/^\s*$/)) {
              throw new Error("The '".concat(name, "' argument should not be empty."));
            }
          }
        }, {
          key: "isIn",
          value: function isIn(val, values, name) {
            // TypeScript enums have keys for **both** the name and the value of each enum member on the type itself.
            if (!(val in values)) {
              throw new Error("Unknown ".concat(name, " value: ").concat(val, "."));
            }
          }
        }]);
        return Arg;
      }();
      /** @private */
      var Platform = /*#__PURE__*/function () {
        function Platform() {
          _classCallCheck(this, Platform);
        }
        _createClass(Platform, null, [{
          key: "isBrowser",
          get:
          // react-native has a window but no document so we should check both
          function get() {
            return (typeof window === "undefined" ? "undefined" : _typeof(window)) === "object" && _typeof(window.document) === "object";
          }
          // WebWorkers don't have a window object so the isBrowser check would fail
        }, {
          key: "isWebWorker",
          get: function get() {
            return (typeof self === "undefined" ? "undefined" : _typeof(self)) === "object" && "importScripts" in self;
          }
          // react-native has a window but no document
        }, {
          key: "isReactNative",
          get: function get() {
            return (typeof window === "undefined" ? "undefined" : _typeof(window)) === "object" && typeof window.document === "undefined";
          }
          // Node apps shouldn't have a window object, but WebWorkers don't either
          // so we need to check for both WebWorker and window
        }, {
          key: "isNode",
          get: function get() {
            return !this.isBrowser && !this.isWebWorker && !this.isReactNative;
          }
        }]);
        return Platform;
      }();
      /** @private */
      function getDataDetail(data, includeContent) {
        var detail = "";
        if (isArrayBuffer(data)) {
          detail = "Binary data of length ".concat(data.byteLength);
          if (includeContent) {
            detail += ". Content: '".concat(formatArrayBuffer(data), "'");
          }
        } else if (typeof data === "string") {
          detail = "String data of length ".concat(data.length);
          if (includeContent) {
            detail += ". Content: '".concat(data, "'");
          }
        }
        return detail;
      }
      /** @private */
      function formatArrayBuffer(data) {
        var view = new Uint8Array(data);
        // Uint8Array.map only supports returning another Uint8Array?
        var str = "";
        view.forEach(function (num) {
          var pad = num < 16 ? "0" : "";
          str += "0x".concat(pad).concat(num.toString(16), " ");
        });
        // Trim of trailing space.
        return str.substr(0, str.length - 1);
      }
      // Also in signalr-protocol-msgpack/Utils.ts
      /** @private */
      function isArrayBuffer(val) {
        return val && typeof ArrayBuffer !== "undefined" && (val instanceof ArrayBuffer ||
        // Sometimes we get an ArrayBuffer that doesn't satisfy instanceof
        val.constructor && val.constructor.name === "ArrayBuffer");
      }
      /** @private */
      function sendMessage(_x, _x2, _x3, _x4, _x5, _x6, _x7) {
        return _sendMessage.apply(this, arguments);
      }
      /** @private */
      function _sendMessage() {
        _sendMessage = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee22(logger, transportName, httpClient, url, accessTokenFactory, content, options) {
          var headers, token, _getUserAgentHeader11, _getUserAgentHeader12, name, value, responseType, response;
          return _regeneratorRuntime().wrap(function _callee22$(_context23) {
            while (1) switch (_context23.prev = _context23.next) {
              case 0:
                headers = {};
                if (!accessTokenFactory) {
                  _context23.next = 6;
                  break;
                }
                _context23.next = 4;
                return accessTokenFactory();
              case 4:
                token = _context23.sent;
                if (token) {
                  headers = _defineProperty({}, "Authorization", "Bearer ".concat(token));
                }
              case 6:
                _getUserAgentHeader11 = getUserAgentHeader(), _getUserAgentHeader12 = _slicedToArray(_getUserAgentHeader11, 2), name = _getUserAgentHeader12[0], value = _getUserAgentHeader12[1];
                headers[name] = value;
                logger.log(_LogLevel.Trace, "(".concat(transportName, " transport) sending data. ").concat(getDataDetail(content, options.logMessageContent), "."));
                responseType = isArrayBuffer(content) ? "arraybuffer" : "text";
                _context23.next = 12;
                return httpClient.post(url, {
                  content: content,
                  headers: _objectSpread(_objectSpread({}, headers), options.headers),
                  responseType: responseType,
                  timeout: options.timeout,
                  withCredentials: options.withCredentials
                });
              case 12:
                response = _context23.sent;
                logger.log(_LogLevel.Trace, "(".concat(transportName, " transport) request complete. Response status: ").concat(response.statusCode, "."));
              case 14:
              case "end":
                return _context23.stop();
            }
          }, _callee22);
        }));
        return _sendMessage.apply(this, arguments);
      }
      function createLogger(logger) {
        if (logger === undefined) {
          return new ConsoleLogger(_LogLevel.Information);
        }
        if (logger === null) {
          return _NullLogger.instance;
        }
        if (logger.log !== undefined) {
          return logger;
        }
        return new ConsoleLogger(logger);
      }
      /** @private */
      var SubjectSubscription = /*#__PURE__*/function () {
        function SubjectSubscription(subject, observer) {
          _classCallCheck(this, SubjectSubscription);
          this._subject = subject;
          this._observer = observer;
        }
        _createClass(SubjectSubscription, [{
          key: "dispose",
          value: function dispose() {
            var index = this._subject.observers.indexOf(this._observer);
            if (index > -1) {
              this._subject.observers.splice(index, 1);
            }
            if (this._subject.observers.length === 0 && this._subject.cancelCallback) {
              this._subject.cancelCallback()["catch"](function (_) {});
            }
          }
        }]);
        return SubjectSubscription;
      }();
      /** @private */
      var ConsoleLogger = /*#__PURE__*/function () {
        function ConsoleLogger(minimumLogLevel) {
          _classCallCheck(this, ConsoleLogger);
          this._minLevel = minimumLogLevel;
          this.out = console;
        }
        _createClass(ConsoleLogger, [{
          key: "log",
          value: function log(logLevel, message) {
            if (logLevel >= this._minLevel) {
              var msg = "[".concat(new Date().toISOString(), "] ").concat(_LogLevel[logLevel], ": ").concat(message);
              switch (logLevel) {
                case _LogLevel.Critical:
                case _LogLevel.Error:
                  this.out.error(msg);
                  break;
                case _LogLevel.Warning:
                  this.out.warn(msg);
                  break;
                case _LogLevel.Information:
                  this.out.info(msg);
                  break;
                default:
                  // console.debug only goes to attached debuggers in Node, so we use console.log for Trace and Debug
                  this.out.log(msg);
                  break;
              }
            }
          }
        }]);
        return ConsoleLogger;
      }();
      /** @private */
      function getUserAgentHeader() {
        var userAgentHeaderName = "X-SignalR-User-Agent";
        if (Platform.isNode) {
          userAgentHeaderName = "User-Agent";
        }
        return [userAgentHeaderName, constructUserAgent(_VERSION, getOsName(), getRuntime(), getRuntimeVersion())];
      }
      /** @private */
      function constructUserAgent(version, os, runtime, runtimeVersion) {
        // Microsoft SignalR/[Version] ([Detailed Version]; [Operating System]; [Runtime]; [Runtime Version])
        var userAgent = "Microsoft SignalR/";
        var majorAndMinor = version.split(".");
        userAgent += "".concat(majorAndMinor[0], ".").concat(majorAndMinor[1]);
        userAgent += " (".concat(version, "; ");
        if (os && os !== "") {
          userAgent += "".concat(os, "; ");
        } else {
          userAgent += "Unknown OS; ";
        }
        userAgent += "".concat(runtime);
        if (runtimeVersion) {
          userAgent += "; ".concat(runtimeVersion);
        } else {
          userAgent += "; Unknown Runtime Version";
        }
        userAgent += ")";
        return userAgent;
      }
      // eslint-disable-next-line spaced-comment
      /*#__PURE__*/
      function getOsName() {
        if (Platform.isNode) {
          switch (process.platform) {
            case "win32":
              return "Windows NT";
            case "darwin":
              return "macOS";
            case "linux":
              return "Linux";
            default:
              return process.platform;
          }
        } else {
          return "";
        }
      }
      // eslint-disable-next-line spaced-comment
      /*#__PURE__*/
      function getRuntimeVersion() {
        if (Platform.isNode) {
          return process.versions.node;
        }
        return undefined;
      }
      function getRuntime() {
        if (Platform.isNode) {
          return "NodeJS";
        } else {
          return "Browser";
        }
      }
      /** @private */
      function getErrorString(e) {
        if (e.stack) {
          return e.stack;
        } else if (e.message) {
          return e.message;
        }
        return "".concat(e);
      }
      /** @private */
      function getGlobalThis() {
        // globalThis is semi-new and not available in Node until v12
        if (typeof globalThis !== "undefined") {
          return globalThis;
        }
        if (typeof self !== "undefined") {
          return self;
        }
        if (typeof window !== "undefined") {
          return window;
        }
        if (typeof __webpack_require__.g !== "undefined") {
          return __webpack_require__.g;
        }
        throw new Error("could not find global");
      }
      ; // CONCATENATED MODULE: ./src/FetchHttpClient.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      var FetchHttpClient = /*#__PURE__*/function (_HttpClient2) {
        _inherits(FetchHttpClient, _HttpClient2);
        var _super9 = _createSuper(FetchHttpClient);
        function FetchHttpClient(logger) {
          var _this9;
          _classCallCheck(this, FetchHttpClient);
          _this9 = _super9.call(this);
          _this9._logger = logger;
          if (typeof fetch === "undefined") {
            // In order to ignore the dynamic require in webpack builds we need to do this magic
            // @ts-ignore: TS doesn't know about these names
            var requireFunc = true ? require : 0;
            // Cookies aren't automatically handled in Node so we need to add a CookieJar to preserve cookies across requests
            _this9._jar = new (requireFunc("tough-cookie").CookieJar)();
            _this9._fetchType = requireFunc("node-fetch");
            // node-fetch doesn't have a nice API for getting and setting cookies
            // fetch-cookie will wrap a fetch implementation with a default CookieJar or a provided one
            _this9._fetchType = requireFunc("fetch-cookie")(_this9._fetchType, _this9._jar);
          } else {
            _this9._fetchType = fetch.bind(getGlobalThis());
          }
          if (typeof AbortController === "undefined") {
            // In order to ignore the dynamic require in webpack builds we need to do this magic
            // @ts-ignore: TS doesn't know about these names
            var _requireFunc = true ? require : 0;
            // Node needs EventListener methods on AbortController which our custom polyfill doesn't provide
            _this9._abortControllerType = _requireFunc("abort-controller");
          } else {
            _this9._abortControllerType = AbortController;
          }
          return _this9;
        }
        /** @inheritDoc */
        _createClass(FetchHttpClient, [{
          key: "send",
          value: function () {
            var _send = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee(request) {
              var _this10 = this;
              var abortController, error, timeoutId, msTimeout, response, errorMessage, content, payload;
              return _regeneratorRuntime().wrap(function _callee$(_context) {
                while (1) switch (_context.prev = _context.next) {
                  case 0:
                    if (!(request.abortSignal && request.abortSignal.aborted)) {
                      _context.next = 2;
                      break;
                    }
                    throw new _AbortError();
                  case 2:
                    if (request.method) {
                      _context.next = 4;
                      break;
                    }
                    throw new Error("No method defined.");
                  case 4:
                    if (request.url) {
                      _context.next = 6;
                      break;
                    }
                    throw new Error("No url defined.");
                  case 6:
                    abortController = new this._abortControllerType();
                    // Hook our abortSignal into the abort controller
                    if (request.abortSignal) {
                      request.abortSignal.onabort = function () {
                        abortController.abort();
                        error = new _AbortError();
                      };
                    }
                    // If a timeout has been passed in, setup a timeout to call abort
                    // Type needs to be any to fit window.setTimeout and NodeJS.setTimeout
                    timeoutId = null;
                    if (request.timeout) {
                      msTimeout = request.timeout;
                      timeoutId = setTimeout(function () {
                        abortController.abort();
                        _this10._logger.log(_LogLevel.Warning, "Timeout from HTTP request.");
                        error = new _TimeoutError();
                      }, msTimeout);
                    }
                    _context.prev = 10;
                    _context.next = 13;
                    return this._fetchType(request.url, {
                      body: request.content,
                      cache: "no-cache",
                      credentials: request.withCredentials === true ? "include" : "same-origin",
                      headers: _objectSpread({
                        "Content-Type": "text/plain;charset=UTF-8",
                        "X-Requested-With": "XMLHttpRequest"
                      }, request.headers),
                      method: request.method,
                      mode: "cors",
                      redirect: "follow",
                      signal: abortController.signal
                    });
                  case 13:
                    response = _context.sent;
                    _context.next = 22;
                    break;
                  case 16:
                    _context.prev = 16;
                    _context.t0 = _context["catch"](10);
                    if (!error) {
                      _context.next = 20;
                      break;
                    }
                    throw error;
                  case 20:
                    this._logger.log(_LogLevel.Warning, "Error from HTTP request. ".concat(_context.t0, "."));
                    throw _context.t0;
                  case 22:
                    _context.prev = 22;
                    if (timeoutId) {
                      clearTimeout(timeoutId);
                    }
                    if (request.abortSignal) {
                      request.abortSignal.onabort = null;
                    }
                    return _context.finish(22);
                  case 26:
                    if (response.ok) {
                      _context.next = 31;
                      break;
                    }
                    _context.next = 29;
                    return deserializeContent(response, "text");
                  case 29:
                    errorMessage = _context.sent;
                    throw new _HttpError(errorMessage || response.statusText, response.status);
                  case 31:
                    content = deserializeContent(response, request.responseType);
                    _context.next = 34;
                    return content;
                  case 34:
                    payload = _context.sent;
                    return _context.abrupt("return", new _HttpResponse(response.status, response.statusText, payload));
                  case 36:
                  case "end":
                    return _context.stop();
                }
              }, _callee, this, [[10, 16, 22, 26]]);
            }));
            function send(_x8) {
              return _send.apply(this, arguments);
            }
            return send;
          }()
        }, {
          key: "getCookieString",
          value: function getCookieString(url) {
            var cookies = "";
            if (Platform.isNode && this._jar) {
              // @ts-ignore: unused variable
              this._jar.getCookies(url, function (e, c) {
                return cookies = c.join("; ");
              });
            }
            return cookies;
          }
        }]);
        return FetchHttpClient;
      }(_HttpClient);
      function deserializeContent(response, responseType) {
        var content;
        switch (responseType) {
          case "arraybuffer":
            content = response.arrayBuffer();
            break;
          case "text":
            content = response.text();
            break;
          case "blob":
          case "document":
          case "json":
            throw new Error("".concat(responseType, " is not supported."));
          default:
            content = response.text();
            break;
        }
        return content;
      }
      ; // CONCATENATED MODULE: ./src/XhrHttpClient.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      var XhrHttpClient = /*#__PURE__*/function (_HttpClient3) {
        _inherits(XhrHttpClient, _HttpClient3);
        var _super10 = _createSuper(XhrHttpClient);
        function XhrHttpClient(logger) {
          var _this11;
          _classCallCheck(this, XhrHttpClient);
          _this11 = _super10.call(this);
          _this11._logger = logger;
          return _this11;
        }
        /** @inheritDoc */
        _createClass(XhrHttpClient, [{
          key: "send",
          value: function send(request) {
            var _this12 = this;
            // Check that abort was not signaled before calling send
            if (request.abortSignal && request.abortSignal.aborted) {
              return Promise.reject(new _AbortError());
            }
            if (!request.method) {
              return Promise.reject(new Error("No method defined."));
            }
            if (!request.url) {
              return Promise.reject(new Error("No url defined."));
            }
            return new Promise(function (resolve, reject) {
              var xhr = new XMLHttpRequest();
              xhr.open(request.method, request.url, true);
              xhr.withCredentials = request.withCredentials === undefined ? true : request.withCredentials;
              xhr.setRequestHeader("X-Requested-With", "XMLHttpRequest");
              // Explicitly setting the Content-Type header for React Native on Android platform.
              xhr.setRequestHeader("Content-Type", "text/plain;charset=UTF-8");
              var headers = request.headers;
              if (headers) {
                Object.keys(headers).forEach(function (header) {
                  xhr.setRequestHeader(header, headers[header]);
                });
              }
              if (request.responseType) {
                xhr.responseType = request.responseType;
              }
              if (request.abortSignal) {
                request.abortSignal.onabort = function () {
                  xhr.abort();
                  reject(new _AbortError());
                };
              }
              if (request.timeout) {
                xhr.timeout = request.timeout;
              }
              xhr.onload = function () {
                if (request.abortSignal) {
                  request.abortSignal.onabort = null;
                }
                if (xhr.status >= 200 && xhr.status < 300) {
                  resolve(new _HttpResponse(xhr.status, xhr.statusText, xhr.response || xhr.responseText));
                } else {
                  reject(new _HttpError(xhr.response || xhr.responseText || xhr.statusText, xhr.status));
                }
              };
              xhr.onerror = function () {
                _this12._logger.log(_LogLevel.Warning, "Error from HTTP request. ".concat(xhr.status, ": ").concat(xhr.statusText, "."));
                reject(new _HttpError(xhr.statusText, xhr.status));
              };
              xhr.ontimeout = function () {
                _this12._logger.log(_LogLevel.Warning, "Timeout from HTTP request.");
                reject(new _TimeoutError());
              };
              xhr.send(request.content || "");
            });
          }
        }]);
        return XhrHttpClient;
      }(_HttpClient);
      ; // CONCATENATED MODULE: ./src/DefaultHttpClient.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      /** Default implementation of {@link @microsoft/signalr.HttpClient}. */
      var _DefaultHttpClient = /*#__PURE__*/function (_HttpClient4) {
        _inherits(_DefaultHttpClient, _HttpClient4);
        var _super11 = _createSuper(_DefaultHttpClient);
        /** Creates a new instance of the {@link @microsoft/signalr.DefaultHttpClient}, using the provided {@link @microsoft/signalr.ILogger} to log messages. */
        function _DefaultHttpClient(logger) {
          var _this13;
          _classCallCheck(this, _DefaultHttpClient);
          _this13 = _super11.call(this);
          if (typeof fetch !== "undefined" && typeof AbortController !== "undefined") {
            _this13._httpClient = new FetchHttpClient(logger);
          } else if (typeof XMLHttpRequest !== "undefined") {
            _this13._httpClient = new XhrHttpClient(logger);
          } else {
            throw new Error("No usable HttpClient found.");
          }
          return _this13;
        }
        /** @inheritDoc */
        _createClass(_DefaultHttpClient, [{
          key: "send",
          value: function send(request) {
            // Check that abort was not signaled before calling send
            if (request.abortSignal && request.abortSignal.aborted) {
              return Promise.reject(new _AbortError());
            }
            if (!request.method) {
              return Promise.reject(new Error("No method defined."));
            }
            if (!request.url) {
              return Promise.reject(new Error("No url defined."));
            }
            return this._httpClient.send(request);
          }
        }, {
          key: "getCookieString",
          value: function getCookieString(url) {
            return this._httpClient.getCookieString(url);
          }
        }]);
        return _DefaultHttpClient;
      }(_HttpClient);
      ; // CONCATENATED MODULE: ./src/TextMessageFormat.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // Not exported from index
      /** @private */
      var TextMessageFormat = /*#__PURE__*/function () {
        function TextMessageFormat() {
          _classCallCheck(this, TextMessageFormat);
        }
        _createClass(TextMessageFormat, null, [{
          key: "write",
          value: function write(output) {
            return "".concat(output).concat(TextMessageFormat.RecordSeparator);
          }
        }, {
          key: "parse",
          value: function parse(input) {
            if (input[input.length - 1] !== TextMessageFormat.RecordSeparator) {
              throw new Error("Message is incomplete.");
            }
            var messages = input.split(TextMessageFormat.RecordSeparator);
            messages.pop();
            return messages;
          }
        }]);
        return TextMessageFormat;
      }();
      TextMessageFormat.RecordSeparatorCode = 0x1e;
      TextMessageFormat.RecordSeparator = String.fromCharCode(TextMessageFormat.RecordSeparatorCode);
      ; // CONCATENATED MODULE: ./src/HandshakeProtocol.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      /** @private */
      var HandshakeProtocol = /*#__PURE__*/function () {
        function HandshakeProtocol() {
          _classCallCheck(this, HandshakeProtocol);
        }
        _createClass(HandshakeProtocol, [{
          key: "writeHandshakeRequest",
          value:
          // Handshake request is always JSON
          function writeHandshakeRequest(handshakeRequest) {
            return TextMessageFormat.write(JSON.stringify(handshakeRequest));
          }
        }, {
          key: "parseHandshakeResponse",
          value: function parseHandshakeResponse(data) {
            var messageData;
            var remainingData;
            if (isArrayBuffer(data)) {
              // Format is binary but still need to read JSON text from handshake response
              var binaryData = new Uint8Array(data);
              var separatorIndex = binaryData.indexOf(TextMessageFormat.RecordSeparatorCode);
              if (separatorIndex === -1) {
                throw new Error("Message is incomplete.");
              }
              // content before separator is handshake response
              // optional content after is additional messages
              var responseLength = separatorIndex + 1;
              messageData = String.fromCharCode.apply(null, Array.prototype.slice.call(binaryData.slice(0, responseLength)));
              remainingData = binaryData.byteLength > responseLength ? binaryData.slice(responseLength).buffer : null;
            } else {
              var textData = data;
              var _separatorIndex = textData.indexOf(TextMessageFormat.RecordSeparator);
              if (_separatorIndex === -1) {
                throw new Error("Message is incomplete.");
              }
              // content before separator is handshake response
              // optional content after is additional messages
              var _responseLength = _separatorIndex + 1;
              messageData = textData.substring(0, _responseLength);
              remainingData = textData.length > _responseLength ? textData.substring(_responseLength) : null;
            }
            // At this point we should have just the single handshake message
            var messages = TextMessageFormat.parse(messageData);
            var response = JSON.parse(messages[0]);
            if (response.type) {
              throw new Error("Expected a handshake response from the server.");
            }
            var responseMessage = response;
            // multiple messages could have arrived with handshake
            // return additional data to be parsed as usual, or null if all parsed
            return [remainingData, responseMessage];
          }
        }]);
        return HandshakeProtocol;
      }();
      ; // CONCATENATED MODULE: ./src/IHubProtocol.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      /** Defines the type of a Hub Message. */
      var _MessageType;
      (function (MessageType) {
        /** Indicates the message is an Invocation message and implements the {@link @microsoft/signalr.InvocationMessage} interface. */
        MessageType[MessageType["Invocation"] = 1] = "Invocation";
        /** Indicates the message is a StreamItem message and implements the {@link @microsoft/signalr.StreamItemMessage} interface. */
        MessageType[MessageType["StreamItem"] = 2] = "StreamItem";
        /** Indicates the message is a Completion message and implements the {@link @microsoft/signalr.CompletionMessage} interface. */
        MessageType[MessageType["Completion"] = 3] = "Completion";
        /** Indicates the message is a Stream Invocation message and implements the {@link @microsoft/signalr.StreamInvocationMessage} interface. */
        MessageType[MessageType["StreamInvocation"] = 4] = "StreamInvocation";
        /** Indicates the message is a Cancel Invocation message and implements the {@link @microsoft/signalr.CancelInvocationMessage} interface. */
        MessageType[MessageType["CancelInvocation"] = 5] = "CancelInvocation";
        /** Indicates the message is a Ping message and implements the {@link @microsoft/signalr.PingMessage} interface. */
        MessageType[MessageType["Ping"] = 6] = "Ping";
        /** Indicates the message is a Close message and implements the {@link @microsoft/signalr.CloseMessage} interface. */
        MessageType[MessageType["Close"] = 7] = "Close";
      })(_MessageType || (_MessageType = {}));
      ; // CONCATENATED MODULE: ./src/Subject.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      /** Stream implementation to stream items to the server. */
      var _Subject = /*#__PURE__*/function () {
        function _Subject() {
          _classCallCheck(this, _Subject);
          this.observers = [];
        }
        _createClass(_Subject, [{
          key: "next",
          value: function next(item) {
            var _iterator = _createForOfIteratorHelper(this.observers),
              _step;
            try {
              for (_iterator.s(); !(_step = _iterator.n()).done;) {
                var observer = _step.value;
                observer.next(item);
              }
            } catch (err) {
              _iterator.e(err);
            } finally {
              _iterator.f();
            }
          }
        }, {
          key: "error",
          value: function error(err) {
            var _iterator2 = _createForOfIteratorHelper(this.observers),
              _step2;
            try {
              for (_iterator2.s(); !(_step2 = _iterator2.n()).done;) {
                var observer = _step2.value;
                if (observer.error) {
                  observer.error(err);
                }
              }
            } catch (err) {
              _iterator2.e(err);
            } finally {
              _iterator2.f();
            }
          }
        }, {
          key: "complete",
          value: function complete() {
            var _iterator3 = _createForOfIteratorHelper(this.observers),
              _step3;
            try {
              for (_iterator3.s(); !(_step3 = _iterator3.n()).done;) {
                var observer = _step3.value;
                if (observer.complete) {
                  observer.complete();
                }
              }
            } catch (err) {
              _iterator3.e(err);
            } finally {
              _iterator3.f();
            }
          }
        }, {
          key: "subscribe",
          value: function subscribe(observer) {
            this.observers.push(observer);
            return new SubjectSubscription(this, observer);
          }
        }]);
        return _Subject;
      }();
      ; // CONCATENATED MODULE: ./src/HubConnection.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      var DEFAULT_TIMEOUT_IN_MS = 30 * 1000;
      var DEFAULT_PING_INTERVAL_IN_MS = 15 * 1000;
      /** Describes the current state of the {@link HubConnection} to the server. */
      var _HubConnectionState;
      (function (HubConnectionState) {
        /** The hub connection is disconnected. */
        HubConnectionState["Disconnected"] = "Disconnected";
        /** The hub connection is connecting. */
        HubConnectionState["Connecting"] = "Connecting";
        /** The hub connection is connected. */
        HubConnectionState["Connected"] = "Connected";
        /** The hub connection is disconnecting. */
        HubConnectionState["Disconnecting"] = "Disconnecting";
        /** The hub connection is reconnecting. */
        HubConnectionState["Reconnecting"] = "Reconnecting";
      })(_HubConnectionState || (_HubConnectionState = {}));
      /** Represents a connection to a SignalR Hub. */
      var _HubConnection = /*#__PURE__*/function () {
        function _HubConnection(connection, logger, protocol, reconnectPolicy) {
          var _this14 = this;
          _classCallCheck(this, _HubConnection);
          this._nextKeepAlive = 0;
          this._freezeEventListener = function () {
            _this14._logger.log(_LogLevel.Warning, "The page is being frozen, this will likely lead to the connection being closed and messages being lost. For more information see the docs at https://docs.microsoft.com/aspnet/core/signalr/javascript-client#bsleep");
          };
          Arg.isRequired(connection, "connection");
          Arg.isRequired(logger, "logger");
          Arg.isRequired(protocol, "protocol");
          this.serverTimeoutInMilliseconds = DEFAULT_TIMEOUT_IN_MS;
          this.keepAliveIntervalInMilliseconds = DEFAULT_PING_INTERVAL_IN_MS;
          this._logger = logger;
          this._protocol = protocol;
          this.connection = connection;
          this._reconnectPolicy = reconnectPolicy;
          this._handshakeProtocol = new HandshakeProtocol();
          this.connection.onreceive = function (data) {
            return _this14._processIncomingData(data);
          };
          this.connection.onclose = function (error) {
            return _this14._connectionClosed(error);
          };
          this._callbacks = {};
          this._methods = {};
          this._closedCallbacks = [];
          this._reconnectingCallbacks = [];
          this._reconnectedCallbacks = [];
          this._invocationId = 0;
          this._receivedHandshakeResponse = false;
          this._connectionState = _HubConnectionState.Disconnected;
          this._connectionStarted = false;
          this._cachedPingMessage = this._protocol.writeMessage({
            type: _MessageType.Ping
          });
        }
        /** @internal */
        // Using a public static factory method means we can have a private constructor and an _internal_
        // create method that can be used by HubConnectionBuilder. An "internal" constructor would just
        // be stripped away and the '.d.ts' file would have no constructor, which is interpreted as a
        // public parameter-less constructor.
        _createClass(_HubConnection, [{
          key: "state",
          get: /** Indicates the state of the {@link HubConnection} to the server. */
          function get() {
            return this._connectionState;
          }
          /** Represents the connection id of the {@link HubConnection} on the server. The connection id will be null when the connection is either
           *  in the disconnected state or if the negotiation step was skipped.
           */
        }, {
          key: "connectionId",
          get: function get() {
            return this.connection ? this.connection.connectionId || null : null;
          }
          /** Indicates the url of the {@link HubConnection} to the server. */
        }, {
          key: "baseUrl",
          get: function get() {
            return this.connection.baseUrl || "";
          }
          /**
           * Sets a new url for the HubConnection. Note that the url can only be changed when the connection is in either the Disconnected or
           * Reconnecting states.
           * @param {string} url The url to connect to.
           */,
          set: function set(url) {
            if (this._connectionState !== _HubConnectionState.Disconnected && this._connectionState !== _HubConnectionState.Reconnecting) {
              throw new Error("The HubConnection must be in the Disconnected or Reconnecting state to change the url.");
            }
            if (!url) {
              throw new Error("The HubConnection url must be a valid url.");
            }
            this.connection.baseUrl = url;
          }
          /** Starts the connection.
           *
           * @returns {Promise<void>} A Promise that resolves when the connection has been successfully established, or rejects with an error.
           */
        }, {
          key: "start",
          value: function start() {
            this._startPromise = this._startWithStateTransitions();
            return this._startPromise;
          }
        }, {
          key: "_startWithStateTransitions",
          value: function () {
            var _startWithStateTransitions2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee2() {
              return _regeneratorRuntime().wrap(function _callee2$(_context2) {
                while (1) switch (_context2.prev = _context2.next) {
                  case 0:
                    if (!(this._connectionState !== _HubConnectionState.Disconnected)) {
                      _context2.next = 2;
                      break;
                    }
                    return _context2.abrupt("return", Promise.reject(new Error("Cannot start a HubConnection that is not in the 'Disconnected' state.")));
                  case 2:
                    this._connectionState = _HubConnectionState.Connecting;
                    this._logger.log(_LogLevel.Debug, "Starting HubConnection.");
                    _context2.prev = 4;
                    _context2.next = 7;
                    return this._startInternal();
                  case 7:
                    if (Platform.isBrowser) {
                      // Log when the browser freezes the tab so users know why their connection unexpectedly stopped working
                      window.document.addEventListener("freeze", this._freezeEventListener);
                    }
                    this._connectionState = _HubConnectionState.Connected;
                    this._connectionStarted = true;
                    this._logger.log(_LogLevel.Debug, "HubConnection connected successfully.");
                    _context2.next = 18;
                    break;
                  case 13:
                    _context2.prev = 13;
                    _context2.t0 = _context2["catch"](4);
                    this._connectionState = _HubConnectionState.Disconnected;
                    this._logger.log(_LogLevel.Debug, "HubConnection failed to start successfully because of error '".concat(_context2.t0, "'."));
                    return _context2.abrupt("return", Promise.reject(_context2.t0));
                  case 18:
                  case "end":
                    return _context2.stop();
                }
              }, _callee2, this, [[4, 13]]);
            }));
            function _startWithStateTransitions() {
              return _startWithStateTransitions2.apply(this, arguments);
            }
            return _startWithStateTransitions;
          }()
        }, {
          key: "_startInternal",
          value: function () {
            var _startInternal2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee3() {
              var _this15 = this;
              var handshakePromise, handshakeRequest;
              return _regeneratorRuntime().wrap(function _callee3$(_context3) {
                while (1) switch (_context3.prev = _context3.next) {
                  case 0:
                    this._stopDuringStartError = undefined;
                    this._receivedHandshakeResponse = false;
                    // Set up the promise before any connection is (re)started otherwise it could race with received messages
                    handshakePromise = new Promise(function (resolve, reject) {
                      _this15._handshakeResolver = resolve;
                      _this15._handshakeRejecter = reject;
                    });
                    _context3.next = 5;
                    return this.connection.start(this._protocol.transferFormat);
                  case 5:
                    _context3.prev = 5;
                    handshakeRequest = {
                      protocol: this._protocol.name,
                      version: this._protocol.version
                    };
                    this._logger.log(_LogLevel.Debug, "Sending handshake request.");
                    _context3.next = 10;
                    return this._sendMessage(this._handshakeProtocol.writeHandshakeRequest(handshakeRequest));
                  case 10:
                    this._logger.log(_LogLevel.Information, "Using HubProtocol '".concat(this._protocol.name, "'."));
                    // defensively cleanup timeout in case we receive a message from the server before we finish start
                    this._cleanupTimeout();
                    this._resetTimeoutPeriod();
                    this._resetKeepAliveInterval();
                    _context3.next = 16;
                    return handshakePromise;
                  case 16:
                    if (!this._stopDuringStartError) {
                      _context3.next = 18;
                      break;
                    }
                    throw this._stopDuringStartError;
                  case 18:
                    _context3.next = 28;
                    break;
                  case 20:
                    _context3.prev = 20;
                    _context3.t0 = _context3["catch"](5);
                    this._logger.log(_LogLevel.Debug, "Hub handshake failed with error '".concat(_context3.t0, "' during start(). Stopping HubConnection."));
                    this._cleanupTimeout();
                    this._cleanupPingTimer();
                    // HttpConnection.stop() should not complete until after the onclose callback is invoked.
                    // This will transition the HubConnection to the disconnected state before HttpConnection.stop() completes.
                    _context3.next = 27;
                    return this.connection.stop(_context3.t0);
                  case 27:
                    throw _context3.t0;
                  case 28:
                  case "end":
                    return _context3.stop();
                }
              }, _callee3, this, [[5, 20]]);
            }));
            function _startInternal() {
              return _startInternal2.apply(this, arguments);
            }
            return _startInternal;
          }()
          /** Stops the connection.
           *
           * @returns {Promise<void>} A Promise that resolves when the connection has been successfully terminated, or rejects with an error.
           */
        }, {
          key: "stop",
          value: function () {
            var _stop = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee4() {
              var startPromise;
              return _regeneratorRuntime().wrap(function _callee4$(_context4) {
                while (1) switch (_context4.prev = _context4.next) {
                  case 0:
                    // Capture the start promise before the connection might be restarted in an onclose callback.
                    startPromise = this._startPromise;
                    this._stopPromise = this._stopInternal();
                    _context4.next = 4;
                    return this._stopPromise;
                  case 4:
                    _context4.prev = 4;
                    _context4.next = 7;
                    return startPromise;
                  case 7:
                    _context4.next = 11;
                    break;
                  case 9:
                    _context4.prev = 9;
                    _context4.t0 = _context4["catch"](4);
                  case 11:
                  case "end":
                    return _context4.stop();
                }
              }, _callee4, this, [[4, 9]]);
            }));
            function stop() {
              return _stop.apply(this, arguments);
            }
            return stop;
          }()
        }, {
          key: "_stopInternal",
          value: function _stopInternal(error) {
            if (this._connectionState === _HubConnectionState.Disconnected) {
              this._logger.log(_LogLevel.Debug, "Call to HubConnection.stop(".concat(error, ") ignored because it is already in the disconnected state."));
              return Promise.resolve();
            }
            if (this._connectionState === _HubConnectionState.Disconnecting) {
              this._logger.log(_LogLevel.Debug, "Call to HttpConnection.stop(".concat(error, ") ignored because the connection is already in the disconnecting state."));
              return this._stopPromise;
            }
            this._connectionState = _HubConnectionState.Disconnecting;
            this._logger.log(_LogLevel.Debug, "Stopping HubConnection.");
            if (this._reconnectDelayHandle) {
              // We're in a reconnect delay which means the underlying connection is currently already stopped.
              // Just clear the handle to stop the reconnect loop (which no one is waiting on thankfully) and
              // fire the onclose callbacks.
              this._logger.log(_LogLevel.Debug, "Connection stopped during reconnect delay. Done reconnecting.");
              clearTimeout(this._reconnectDelayHandle);
              this._reconnectDelayHandle = undefined;
              this._completeClose();
              return Promise.resolve();
            }
            this._cleanupTimeout();
            this._cleanupPingTimer();
            this._stopDuringStartError = error || new Error("The connection was stopped before the hub handshake could complete.");
            // HttpConnection.stop() should not complete until after either HttpConnection.start() fails
            // or the onclose callback is invoked. The onclose callback will transition the HubConnection
            // to the disconnected state if need be before HttpConnection.stop() completes.
            return this.connection.stop(error);
          }
          /** Invokes a streaming hub method on the server using the specified name and arguments.
           *
           * @typeparam T The type of the items returned by the server.
           * @param {string} methodName The name of the server method to invoke.
           * @param {any[]} args The arguments used to invoke the server method.
           * @returns {IStreamResult<T>} An object that yields results from the server as they are received.
           */
        }, {
          key: "stream",
          value: function stream(methodName) {
            var _this16 = this;
            for (var _len = arguments.length, args = new Array(_len > 1 ? _len - 1 : 0), _key = 1; _key < _len; _key++) {
              args[_key - 1] = arguments[_key];
            }
            var _this$_replaceStreami = this._replaceStreamingParams(args),
              _this$_replaceStreami2 = _slicedToArray(_this$_replaceStreami, 2),
              streams = _this$_replaceStreami2[0],
              streamIds = _this$_replaceStreami2[1];
            var invocationDescriptor = this._createStreamInvocation(methodName, args, streamIds);
            // eslint-disable-next-line prefer-const
            var promiseQueue;
            var subject = new _Subject();
            subject.cancelCallback = function () {
              var cancelInvocation = _this16._createCancelInvocation(invocationDescriptor.invocationId);
              delete _this16._callbacks[invocationDescriptor.invocationId];
              return promiseQueue.then(function () {
                return _this16._sendWithProtocol(cancelInvocation);
              });
            };
            this._callbacks[invocationDescriptor.invocationId] = function (invocationEvent, error) {
              if (error) {
                subject.error(error);
                return;
              } else if (invocationEvent) {
                // invocationEvent will not be null when an error is not passed to the callback
                if (invocationEvent.type === _MessageType.Completion) {
                  if (invocationEvent.error) {
                    subject.error(new Error(invocationEvent.error));
                  } else {
                    subject.complete();
                  }
                } else {
                  subject.next(invocationEvent.item);
                }
              }
            };
            promiseQueue = this._sendWithProtocol(invocationDescriptor)["catch"](function (e) {
              subject.error(e);
              delete _this16._callbacks[invocationDescriptor.invocationId];
            });
            this._launchStreams(streams, promiseQueue);
            return subject;
          }
        }, {
          key: "_sendMessage",
          value: function _sendMessage(message) {
            this._resetKeepAliveInterval();
            return this.connection.send(message);
          }
          /**
           * Sends a js object to the server.
           * @param message The js object to serialize and send.
           */
        }, {
          key: "_sendWithProtocol",
          value: function _sendWithProtocol(message) {
            return this._sendMessage(this._protocol.writeMessage(message));
          }
          /** Invokes a hub method on the server using the specified name and arguments. Does not wait for a response from the receiver.
           *
           * The Promise returned by this method resolves when the client has sent the invocation to the server. The server may still
           * be processing the invocation.
           *
           * @param {string} methodName The name of the server method to invoke.
           * @param {any[]} args The arguments used to invoke the server method.
           * @returns {Promise<void>} A Promise that resolves when the invocation has been successfully sent, or rejects with an error.
           */
        }, {
          key: "send",
          value: function send(methodName) {
            for (var _len2 = arguments.length, args = new Array(_len2 > 1 ? _len2 - 1 : 0), _key2 = 1; _key2 < _len2; _key2++) {
              args[_key2 - 1] = arguments[_key2];
            }
            var _this$_replaceStreami3 = this._replaceStreamingParams(args),
              _this$_replaceStreami4 = _slicedToArray(_this$_replaceStreami3, 2),
              streams = _this$_replaceStreami4[0],
              streamIds = _this$_replaceStreami4[1];
            var sendPromise = this._sendWithProtocol(this._createInvocation(methodName, args, true, streamIds));
            this._launchStreams(streams, sendPromise);
            return sendPromise;
          }
          /** Invokes a hub method on the server using the specified name and arguments.
           *
           * The Promise returned by this method resolves when the server indicates it has finished invoking the method. When the promise
           * resolves, the server has finished invoking the method. If the server method returns a result, it is produced as the result of
           * resolving the Promise.
           *
           * @typeparam T The expected return type.
           * @param {string} methodName The name of the server method to invoke.
           * @param {any[]} args The arguments used to invoke the server method.
           * @returns {Promise<T>} A Promise that resolves with the result of the server method (if any), or rejects with an error.
           */
        }, {
          key: "invoke",
          value: function invoke(methodName) {
            var _this17 = this;
            for (var _len3 = arguments.length, args = new Array(_len3 > 1 ? _len3 - 1 : 0), _key3 = 1; _key3 < _len3; _key3++) {
              args[_key3 - 1] = arguments[_key3];
            }
            var _this$_replaceStreami5 = this._replaceStreamingParams(args),
              _this$_replaceStreami6 = _slicedToArray(_this$_replaceStreami5, 2),
              streams = _this$_replaceStreami6[0],
              streamIds = _this$_replaceStreami6[1];
            var invocationDescriptor = this._createInvocation(methodName, args, false, streamIds);
            var p = new Promise(function (resolve, reject) {
              // invocationId will always have a value for a non-blocking invocation
              _this17._callbacks[invocationDescriptor.invocationId] = function (invocationEvent, error) {
                if (error) {
                  reject(error);
                  return;
                } else if (invocationEvent) {
                  // invocationEvent will not be null when an error is not passed to the callback
                  if (invocationEvent.type === _MessageType.Completion) {
                    if (invocationEvent.error) {
                      reject(new Error(invocationEvent.error));
                    } else {
                      resolve(invocationEvent.result);
                    }
                  } else {
                    reject(new Error("Unexpected message type: ".concat(invocationEvent.type)));
                  }
                }
              };
              var promiseQueue = _this17._sendWithProtocol(invocationDescriptor)["catch"](function (e) {
                reject(e);
                // invocationId will always have a value for a non-blocking invocation
                delete _this17._callbacks[invocationDescriptor.invocationId];
              });
              _this17._launchStreams(streams, promiseQueue);
            });
            return p;
          }
          /** Registers a handler that will be invoked when the hub method with the specified method name is invoked.
           *
           * @param {string} methodName The name of the hub method to define.
           * @param {Function} newMethod The handler that will be raised when the hub method is invoked.
           */
        }, {
          key: "on",
          value: function on(methodName, newMethod) {
            if (!methodName || !newMethod) {
              return;
            }
            methodName = methodName.toLowerCase();
            if (!this._methods[methodName]) {
              this._methods[methodName] = [];
            }
            // Preventing adding the same handler multiple times.
            if (this._methods[methodName].indexOf(newMethod) !== -1) {
              return;
            }
            this._methods[methodName].push(newMethod);
          }
        }, {
          key: "off",
          value: function off(methodName, method) {
            if (!methodName) {
              return;
            }
            methodName = methodName.toLowerCase();
            var handlers = this._methods[methodName];
            if (!handlers) {
              return;
            }
            if (method) {
              var removeIdx = handlers.indexOf(method);
              if (removeIdx !== -1) {
                handlers.splice(removeIdx, 1);
                if (handlers.length === 0) {
                  delete this._methods[methodName];
                }
              }
            } else {
              delete this._methods[methodName];
            }
          }
          /** Registers a handler that will be invoked when the connection is closed.
           *
           * @param {Function} callback The handler that will be invoked when the connection is closed. Optionally receives a single argument containing the error that caused the connection to close (if any).
           */
        }, {
          key: "onclose",
          value: function onclose(callback) {
            if (callback) {
              this._closedCallbacks.push(callback);
            }
          }
          /** Registers a handler that will be invoked when the connection starts reconnecting.
           *
           * @param {Function} callback The handler that will be invoked when the connection starts reconnecting. Optionally receives a single argument containing the error that caused the connection to start reconnecting (if any).
           */
        }, {
          key: "onreconnecting",
          value: function onreconnecting(callback) {
            if (callback) {
              this._reconnectingCallbacks.push(callback);
            }
          }
          /** Registers a handler that will be invoked when the connection successfully reconnects.
           *
           * @param {Function} callback The handler that will be invoked when the connection successfully reconnects.
           */
        }, {
          key: "onreconnected",
          value: function onreconnected(callback) {
            if (callback) {
              this._reconnectedCallbacks.push(callback);
            }
          }
        }, {
          key: "_processIncomingData",
          value: function _processIncomingData(data) {
            this._cleanupTimeout();
            if (!this._receivedHandshakeResponse) {
              data = this._processHandshakeResponse(data);
              this._receivedHandshakeResponse = true;
            }
            // Data may have all been read when processing handshake response
            if (data) {
              // Parse the messages
              var messages = this._protocol.parseMessages(data, this._logger);
              var _iterator4 = _createForOfIteratorHelper(messages),
                _step4;
              try {
                for (_iterator4.s(); !(_step4 = _iterator4.n()).done;) {
                  var message = _step4.value;
                  switch (message.type) {
                    case _MessageType.Invocation:
                      this._invokeClientMethod(message);
                      break;
                    case _MessageType.StreamItem:
                    case _MessageType.Completion:
                      {
                        var callback = this._callbacks[message.invocationId];
                        if (callback) {
                          if (message.type === _MessageType.Completion) {
                            delete this._callbacks[message.invocationId];
                          }
                          try {
                            callback(message);
                          } catch (e) {
                            this._logger.log(_LogLevel.Error, "Stream callback threw error: ".concat(getErrorString(e)));
                          }
                        }
                        break;
                      }
                    case _MessageType.Ping:
                      // Don't care about pings
                      break;
                    case _MessageType.Close:
                      {
                        this._logger.log(_LogLevel.Information, "Close message received from server.");
                        var error = message.error ? new Error("Server returned an error on close: " + message.error) : undefined;
                        if (message.allowReconnect === true) {
                          // It feels wrong not to await connection.stop() here, but processIncomingData is called as part of an onreceive callback which is not async,
                          // this is already the behavior for serverTimeout(), and HttpConnection.Stop() should catch and log all possible exceptions.
                          // eslint-disable-next-line @typescript-eslint/no-floating-promises
                          this.connection.stop(error);
                        } else {
                          // We cannot await stopInternal() here, but subsequent calls to stop() will await this if stopInternal() is still ongoing.
                          this._stopPromise = this._stopInternal(error);
                        }
                        break;
                      }
                    default:
                      this._logger.log(_LogLevel.Warning, "Invalid message type: ".concat(message.type, "."));
                      break;
                  }
                }
              } catch (err) {
                _iterator4.e(err);
              } finally {
                _iterator4.f();
              }
            }
            this._resetTimeoutPeriod();
          }
        }, {
          key: "_processHandshakeResponse",
          value: function _processHandshakeResponse(data) {
            var responseMessage;
            var remainingData;
            try {
              var _this$_handshakeProto = this._handshakeProtocol.parseHandshakeResponse(data);
              var _this$_handshakeProto2 = _slicedToArray(_this$_handshakeProto, 2);
              remainingData = _this$_handshakeProto2[0];
              responseMessage = _this$_handshakeProto2[1];
            } catch (e) {
              var message = "Error parsing handshake response: " + e;
              this._logger.log(_LogLevel.Error, message);
              var error = new Error(message);
              this._handshakeRejecter(error);
              throw error;
            }
            if (responseMessage.error) {
              var _message2 = "Server returned handshake error: " + responseMessage.error;
              this._logger.log(_LogLevel.Error, _message2);
              var _error = new Error(_message2);
              this._handshakeRejecter(_error);
              throw _error;
            } else {
              this._logger.log(_LogLevel.Debug, "Server handshake complete.");
            }
            this._handshakeResolver();
            return remainingData;
          }
        }, {
          key: "_resetKeepAliveInterval",
          value: function _resetKeepAliveInterval() {
            if (this.connection.features.inherentKeepAlive) {
              return;
            }
            // Set the time we want the next keep alive to be sent
            // Timer will be setup on next message receive
            this._nextKeepAlive = new Date().getTime() + this.keepAliveIntervalInMilliseconds;
            this._cleanupPingTimer();
          }
        }, {
          key: "_resetTimeoutPeriod",
          value: function _resetTimeoutPeriod() {
            var _this18 = this;
            if (!this.connection.features || !this.connection.features.inherentKeepAlive) {
              // Set the timeout timer
              this._timeoutHandle = setTimeout(function () {
                return _this18.serverTimeout();
              }, this.serverTimeoutInMilliseconds);
              // Set keepAlive timer if there isn't one
              if (this._pingServerHandle === undefined) {
                var nextPing = this._nextKeepAlive - new Date().getTime();
                if (nextPing < 0) {
                  nextPing = 0;
                }
                // The timer needs to be set from a networking callback to avoid Chrome timer throttling from causing timers to run once a minute
                this._pingServerHandle = setTimeout( /*#__PURE__*/_asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee5() {
                  return _regeneratorRuntime().wrap(function _callee5$(_context5) {
                    while (1) switch (_context5.prev = _context5.next) {
                      case 0:
                        if (!(_this18._connectionState === _HubConnectionState.Connected)) {
                          _context5.next = 9;
                          break;
                        }
                        _context5.prev = 1;
                        _context5.next = 4;
                        return _this18._sendMessage(_this18._cachedPingMessage);
                      case 4:
                        _context5.next = 9;
                        break;
                      case 6:
                        _context5.prev = 6;
                        _context5.t0 = _context5["catch"](1);
                        // We don't care about the error. It should be seen elsewhere in the client.
                        // The connection is probably in a bad or closed state now, cleanup the timer so it stops triggering
                        _this18._cleanupPingTimer();
                      case 9:
                      case "end":
                        return _context5.stop();
                    }
                  }, _callee5, null, [[1, 6]]);
                })), nextPing);
              }
            }
          }
          // eslint-disable-next-line @typescript-eslint/naming-convention
        }, {
          key: "serverTimeout",
          value: function serverTimeout() {
            // The server hasn't talked to us in a while. It doesn't like us anymore ... :(
            // Terminate the connection, but we don't need to wait on the promise. This could trigger reconnecting.
            // eslint-disable-next-line @typescript-eslint/no-floating-promises
            this.connection.stop(new Error("Server timeout elapsed without receiving a message from the server."));
          }
        }, {
          key: "_invokeClientMethod",
          value: function _invokeClientMethod(invocationMessage) {
            var _this19 = this;
            var methods = this._methods[invocationMessage.target.toLowerCase()];
            if (methods) {
              try {
                methods.forEach(function (m) {
                  return m.apply(_this19, invocationMessage.arguments);
                });
              } catch (e) {
                this._logger.log(_LogLevel.Error, "A callback for the method ".concat(invocationMessage.target.toLowerCase(), " threw error '").concat(e, "'."));
              }
              if (invocationMessage.invocationId) {
                // This is not supported in v1. So we return an error to avoid blocking the server waiting for the response.
                var message = "Server requested a response, which is not supported in this version of the client.";
                this._logger.log(_LogLevel.Error, message);
                // We don't want to wait on the stop itself.
                this._stopPromise = this._stopInternal(new Error(message));
              }
            } else {
              this._logger.log(_LogLevel.Warning, "No client method with the name '".concat(invocationMessage.target, "' found."));
            }
          }
        }, {
          key: "_connectionClosed",
          value: function _connectionClosed(error) {
            this._logger.log(_LogLevel.Debug, "HubConnection.connectionClosed(".concat(error, ") called while in state ").concat(this._connectionState, "."));
            // Triggering this.handshakeRejecter is insufficient because it could already be resolved without the continuation having run yet.
            this._stopDuringStartError = this._stopDuringStartError || error || new Error("The underlying connection was closed before the hub handshake could complete.");
            // If the handshake is in progress, start will be waiting for the handshake promise, so we complete it.
            // If it has already completed, this should just noop.
            if (this._handshakeResolver) {
              this._handshakeResolver();
            }
            this._cancelCallbacksWithError(error || new Error("Invocation canceled due to the underlying connection being closed."));
            this._cleanupTimeout();
            this._cleanupPingTimer();
            if (this._connectionState === _HubConnectionState.Disconnecting) {
              this._completeClose(error);
            } else if (this._connectionState === _HubConnectionState.Connected && this._reconnectPolicy) {
              // eslint-disable-next-line @typescript-eslint/no-floating-promises
              this._reconnect(error);
            } else if (this._connectionState === _HubConnectionState.Connected) {
              this._completeClose(error);
            }
            // If none of the above if conditions were true were called the HubConnection must be in either:
            // 1. The Connecting state in which case the handshakeResolver will complete it and stopDuringStartError will fail it.
            // 2. The Reconnecting state in which case the handshakeResolver will complete it and stopDuringStartError will fail the current reconnect attempt
            //    and potentially continue the reconnect() loop.
            // 3. The Disconnected state in which case we're already done.
          }
        }, {
          key: "_completeClose",
          value: function _completeClose(error) {
            var _this20 = this;
            if (this._connectionStarted) {
              this._connectionState = _HubConnectionState.Disconnected;
              this._connectionStarted = false;
              if (Platform.isBrowser) {
                window.document.removeEventListener("freeze", this._freezeEventListener);
              }
              try {
                this._closedCallbacks.forEach(function (c) {
                  return c.apply(_this20, [error]);
                });
              } catch (e) {
                this._logger.log(_LogLevel.Error, "An onclose callback called with error '".concat(error, "' threw error '").concat(e, "'."));
              }
            }
          }
        }, {
          key: "_reconnect",
          value: function () {
            var _reconnect2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee6(error) {
              var _this21 = this;
              var reconnectStartTime, previousReconnectAttempts, retryError, nextRetryDelay;
              return _regeneratorRuntime().wrap(function _callee6$(_context6) {
                while (1) switch (_context6.prev = _context6.next) {
                  case 0:
                    reconnectStartTime = Date.now();
                    previousReconnectAttempts = 0;
                    retryError = error !== undefined ? error : new Error("Attempting to reconnect due to a unknown error.");
                    nextRetryDelay = this._getNextRetryDelay(previousReconnectAttempts++, 0, retryError);
                    if (!(nextRetryDelay === null)) {
                      _context6.next = 8;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Connection not reconnecting because the IRetryPolicy returned null on the first reconnect attempt.");
                    this._completeClose(error);
                    return _context6.abrupt("return");
                  case 8:
                    this._connectionState = _HubConnectionState.Reconnecting;
                    if (error) {
                      this._logger.log(_LogLevel.Information, "Connection reconnecting because of error '".concat(error, "'."));
                    } else {
                      this._logger.log(_LogLevel.Information, "Connection reconnecting.");
                    }
                    if (!(this._reconnectingCallbacks.length !== 0)) {
                      _context6.next = 15;
                      break;
                    }
                    try {
                      this._reconnectingCallbacks.forEach(function (c) {
                        return c.apply(_this21, [error]);
                      });
                    } catch (e) {
                      this._logger.log(_LogLevel.Error, "An onreconnecting callback called with error '".concat(error, "' threw error '").concat(e, "'."));
                    }
                    // Exit early if an onreconnecting callback called connection.stop().
                    if (!(this._connectionState !== _HubConnectionState.Reconnecting)) {
                      _context6.next = 15;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Connection left the reconnecting state in onreconnecting callback. Done reconnecting.");
                    return _context6.abrupt("return");
                  case 15:
                    if (!(nextRetryDelay !== null)) {
                      _context6.next = 43;
                      break;
                    }
                    this._logger.log(_LogLevel.Information, "Reconnect attempt number ".concat(previousReconnectAttempts, " will start in ").concat(nextRetryDelay, " ms."));
                    _context6.next = 19;
                    return new Promise(function (resolve) {
                      _this21._reconnectDelayHandle = setTimeout(resolve, nextRetryDelay);
                    });
                  case 19:
                    this._reconnectDelayHandle = undefined;
                    if (!(this._connectionState !== _HubConnectionState.Reconnecting)) {
                      _context6.next = 23;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Connection left the reconnecting state during reconnect delay. Done reconnecting.");
                    return _context6.abrupt("return");
                  case 23:
                    _context6.prev = 23;
                    _context6.next = 26;
                    return this._startInternal();
                  case 26:
                    this._connectionState = _HubConnectionState.Connected;
                    this._logger.log(_LogLevel.Information, "HubConnection reconnected successfully.");
                    if (this._reconnectedCallbacks.length !== 0) {
                      try {
                        this._reconnectedCallbacks.forEach(function (c) {
                          return c.apply(_this21, [_this21.connection.connectionId]);
                        });
                      } catch (e) {
                        this._logger.log(_LogLevel.Error, "An onreconnected callback called with connectionId '".concat(this.connection.connectionId, "; threw error '").concat(e, "'."));
                      }
                    }
                    return _context6.abrupt("return");
                  case 32:
                    _context6.prev = 32;
                    _context6.t0 = _context6["catch"](23);
                    this._logger.log(_LogLevel.Information, "Reconnect attempt failed because of error '".concat(_context6.t0, "'."));
                    if (!(this._connectionState !== _HubConnectionState.Reconnecting)) {
                      _context6.next = 39;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Connection moved to the '".concat(this._connectionState, "' from the reconnecting state during reconnect attempt. Done reconnecting."));
                    // The TypeScript compiler thinks that connectionState must be Connected here. The TypeScript compiler is wrong.
                    if (this._connectionState === _HubConnectionState.Disconnecting) {
                      this._completeClose();
                    }
                    return _context6.abrupt("return");
                  case 39:
                    retryError = _context6.t0 instanceof Error ? _context6.t0 : new Error(_context6.t0.toString());
                    nextRetryDelay = this._getNextRetryDelay(previousReconnectAttempts++, Date.now() - reconnectStartTime, retryError);
                  case 41:
                    _context6.next = 15;
                    break;
                  case 43:
                    this._logger.log(_LogLevel.Information, "Reconnect retries have been exhausted after ".concat(Date.now() - reconnectStartTime, " ms and ").concat(previousReconnectAttempts, " failed attempts. Connection disconnecting."));
                    this._completeClose();
                  case 45:
                  case "end":
                    return _context6.stop();
                }
              }, _callee6, this, [[23, 32]]);
            }));
            function _reconnect(_x9) {
              return _reconnect2.apply(this, arguments);
            }
            return _reconnect;
          }()
        }, {
          key: "_getNextRetryDelay",
          value: function _getNextRetryDelay(previousRetryCount, elapsedMilliseconds, retryReason) {
            try {
              return this._reconnectPolicy.nextRetryDelayInMilliseconds({
                elapsedMilliseconds: elapsedMilliseconds,
                previousRetryCount: previousRetryCount,
                retryReason: retryReason
              });
            } catch (e) {
              this._logger.log(_LogLevel.Error, "IRetryPolicy.nextRetryDelayInMilliseconds(".concat(previousRetryCount, ", ").concat(elapsedMilliseconds, ") threw error '").concat(e, "'."));
              return null;
            }
          }
        }, {
          key: "_cancelCallbacksWithError",
          value: function _cancelCallbacksWithError(error) {
            var _this22 = this;
            var callbacks = this._callbacks;
            this._callbacks = {};
            Object.keys(callbacks).forEach(function (key) {
              var callback = callbacks[key];
              try {
                callback(null, error);
              } catch (e) {
                _this22._logger.log(_LogLevel.Error, "Stream 'error' callback called with '".concat(error, "' threw error: ").concat(getErrorString(e)));
              }
            });
          }
        }, {
          key: "_cleanupPingTimer",
          value: function _cleanupPingTimer() {
            if (this._pingServerHandle) {
              clearTimeout(this._pingServerHandle);
              this._pingServerHandle = undefined;
            }
          }
        }, {
          key: "_cleanupTimeout",
          value: function _cleanupTimeout() {
            if (this._timeoutHandle) {
              clearTimeout(this._timeoutHandle);
            }
          }
        }, {
          key: "_createInvocation",
          value: function _createInvocation(methodName, args, nonblocking, streamIds) {
            if (nonblocking) {
              if (streamIds.length !== 0) {
                return {
                  arguments: args,
                  streamIds: streamIds,
                  target: methodName,
                  type: _MessageType.Invocation
                };
              } else {
                return {
                  arguments: args,
                  target: methodName,
                  type: _MessageType.Invocation
                };
              }
            } else {
              var invocationId = this._invocationId;
              this._invocationId++;
              if (streamIds.length !== 0) {
                return {
                  arguments: args,
                  invocationId: invocationId.toString(),
                  streamIds: streamIds,
                  target: methodName,
                  type: _MessageType.Invocation
                };
              } else {
                return {
                  arguments: args,
                  invocationId: invocationId.toString(),
                  target: methodName,
                  type: _MessageType.Invocation
                };
              }
            }
          }
        }, {
          key: "_launchStreams",
          value: function _launchStreams(streams, promiseQueue) {
            var _this23 = this;
            if (streams.length === 0) {
              return;
            }
            // Synchronize stream data so they arrive in-order on the server
            if (!promiseQueue) {
              promiseQueue = Promise.resolve();
            }
            // We want to iterate over the keys, since the keys are the stream ids
            // eslint-disable-next-line guard-for-in
            var _loop = function _loop(streamId) {
              streams[streamId].subscribe({
                complete: function complete() {
                  promiseQueue = promiseQueue.then(function () {
                    return _this23._sendWithProtocol(_this23._createCompletionMessage(streamId));
                  });
                },
                error: function error(err) {
                  var message;
                  if (err instanceof Error) {
                    message = err.message;
                  } else if (err && err.toString) {
                    message = err.toString();
                  } else {
                    message = "Unknown error";
                  }
                  promiseQueue = promiseQueue.then(function () {
                    return _this23._sendWithProtocol(_this23._createCompletionMessage(streamId, message));
                  });
                },
                next: function next(item) {
                  promiseQueue = promiseQueue.then(function () {
                    return _this23._sendWithProtocol(_this23._createStreamItemMessage(streamId, item));
                  });
                }
              });
            };
            for (var streamId in streams) {
              _loop(streamId);
            }
          }
        }, {
          key: "_replaceStreamingParams",
          value: function _replaceStreamingParams(args) {
            var streams = [];
            var streamIds = [];
            for (var i = 0; i < args.length; i++) {
              var argument = args[i];
              if (this._isObservable(argument)) {
                var streamId = this._invocationId;
                this._invocationId++;
                // Store the stream for later use
                streams[streamId] = argument;
                streamIds.push(streamId.toString());
                // remove stream from args
                args.splice(i, 1);
              }
            }
            return [streams, streamIds];
          }
        }, {
          key: "_isObservable",
          value: function _isObservable(arg) {
            // This allows other stream implementations to just work (like rxjs)
            return arg && arg.subscribe && typeof arg.subscribe === "function";
          }
        }, {
          key: "_createStreamInvocation",
          value: function _createStreamInvocation(methodName, args, streamIds) {
            var invocationId = this._invocationId;
            this._invocationId++;
            if (streamIds.length !== 0) {
              return {
                arguments: args,
                invocationId: invocationId.toString(),
                streamIds: streamIds,
                target: methodName,
                type: _MessageType.StreamInvocation
              };
            } else {
              return {
                arguments: args,
                invocationId: invocationId.toString(),
                target: methodName,
                type: _MessageType.StreamInvocation
              };
            }
          }
        }, {
          key: "_createCancelInvocation",
          value: function _createCancelInvocation(id) {
            return {
              invocationId: id,
              type: _MessageType.CancelInvocation
            };
          }
        }, {
          key: "_createStreamItemMessage",
          value: function _createStreamItemMessage(id, item) {
            return {
              invocationId: id,
              item: item,
              type: _MessageType.StreamItem
            };
          }
        }, {
          key: "_createCompletionMessage",
          value: function _createCompletionMessage(id, error, result) {
            if (error) {
              return {
                error: error,
                invocationId: id,
                type: _MessageType.Completion
              };
            }
            return {
              invocationId: id,
              result: result,
              type: _MessageType.Completion
            };
          }
        }], [{
          key: "create",
          value: function create(connection, logger, protocol, reconnectPolicy) {
            return new _HubConnection(connection, logger, protocol, reconnectPolicy);
          }
        }]);
        return _HubConnection;
      }();
      ; // CONCATENATED MODULE: ./src/DefaultReconnectPolicy.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // 0, 2, 10, 30 second delays before reconnect attempts.
      var DEFAULT_RETRY_DELAYS_IN_MILLISECONDS = [0, 2000, 10000, 30000, null];
      /** @private */
      var DefaultReconnectPolicy = /*#__PURE__*/function () {
        function DefaultReconnectPolicy(retryDelays) {
          _classCallCheck(this, DefaultReconnectPolicy);
          this._retryDelays = retryDelays !== undefined ? [].concat(_toConsumableArray(retryDelays), [null]) : DEFAULT_RETRY_DELAYS_IN_MILLISECONDS;
        }
        _createClass(DefaultReconnectPolicy, [{
          key: "nextRetryDelayInMilliseconds",
          value: function nextRetryDelayInMilliseconds(retryContext) {
            return this._retryDelays[retryContext.previousRetryCount];
          }
        }]);
        return DefaultReconnectPolicy;
      }();
      ; // CONCATENATED MODULE: ./src/HeaderNames.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      var HeaderNames = /*#__PURE__*/_createClass(function HeaderNames() {
        _classCallCheck(this, HeaderNames);
      });
      HeaderNames.Authorization = "Authorization";
      HeaderNames.Cookie = "Cookie";
      ; // CONCATENATED MODULE: ./src/ITransport.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // This will be treated as a bit flag in the future, so we keep it using power-of-two values.
      /** Specifies a specific HTTP transport type. */
      var _HttpTransportType;
      (function (HttpTransportType) {
        /** Specifies no transport preference. */
        HttpTransportType[HttpTransportType["None"] = 0] = "None";
        /** Specifies the WebSockets transport. */
        HttpTransportType[HttpTransportType["WebSockets"] = 1] = "WebSockets";
        /** Specifies the Server-Sent Events transport. */
        HttpTransportType[HttpTransportType["ServerSentEvents"] = 2] = "ServerSentEvents";
        /** Specifies the Long Polling transport. */
        HttpTransportType[HttpTransportType["LongPolling"] = 4] = "LongPolling";
      })(_HttpTransportType || (_HttpTransportType = {}));
      /** Specifies the transfer format for a connection. */
      var _TransferFormat;
      (function (TransferFormat) {
        /** Specifies that only text data will be transmitted over the connection. */
        TransferFormat[TransferFormat["Text"] = 1] = "Text";
        /** Specifies that binary data will be transmitted over the connection. */
        TransferFormat[TransferFormat["Binary"] = 2] = "Binary";
      })(_TransferFormat || (_TransferFormat = {}));
      ; // CONCATENATED MODULE: ./src/AbortController.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // Rough polyfill of https://developer.mozilla.org/en-US/docs/Web/API/AbortController
      // We don't actually ever use the API being polyfilled, we always use the polyfill because
      // it's a very new API right now.
      // Not exported from index.
      /** @private */
      var AbortController_AbortController = /*#__PURE__*/function () {
        function AbortController_AbortController() {
          _classCallCheck(this, AbortController_AbortController);
          this._isAborted = false;
          this.onabort = null;
        }
        _createClass(AbortController_AbortController, [{
          key: "abort",
          value: function abort() {
            if (!this._isAborted) {
              this._isAborted = true;
              if (this.onabort) {
                this.onabort();
              }
            }
          }
        }, {
          key: "signal",
          get: function get() {
            return this;
          }
        }, {
          key: "aborted",
          get: function get() {
            return this._isAborted;
          }
        }]);
        return AbortController_AbortController;
      }();
      ; // CONCATENATED MODULE: ./src/LongPollingTransport.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      // Not exported from 'index', this type is internal.
      /** @private */
      var LongPollingTransport = /*#__PURE__*/function () {
        function LongPollingTransport(httpClient, accessTokenFactory, logger, options) {
          _classCallCheck(this, LongPollingTransport);
          this._httpClient = httpClient;
          this._accessTokenFactory = accessTokenFactory;
          this._logger = logger;
          this._pollAbort = new AbortController_AbortController();
          this._options = options;
          this._running = false;
          this.onreceive = null;
          this.onclose = null;
        }
        // This is an internal type, not exported from 'index' so this is really just internal.
        _createClass(LongPollingTransport, [{
          key: "pollAborted",
          get: function get() {
            return this._pollAbort.aborted;
          }
        }, {
          key: "connect",
          value: function () {
            var _connect = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee7(url, transferFormat) {
              var _getUserAgentHeader, _getUserAgentHeader2, name, value, headers, pollOptions, token, pollUrl, response;
              return _regeneratorRuntime().wrap(function _callee7$(_context7) {
                while (1) switch (_context7.prev = _context7.next) {
                  case 0:
                    Arg.isRequired(url, "url");
                    Arg.isRequired(transferFormat, "transferFormat");
                    Arg.isIn(transferFormat, _TransferFormat, "transferFormat");
                    this._url = url;
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) Connecting.");
                    // Allow binary format on Node and Browsers that support binary content (indicated by the presence of responseType property)
                    if (!(transferFormat === _TransferFormat.Binary && typeof XMLHttpRequest !== "undefined" && typeof new XMLHttpRequest().responseType !== "string")) {
                      _context7.next = 7;
                      break;
                    }
                    throw new Error("Binary protocols over XmlHttpRequest not implementing advanced features are not supported.");
                  case 7:
                    _getUserAgentHeader = getUserAgentHeader(), _getUserAgentHeader2 = _slicedToArray(_getUserAgentHeader, 2), name = _getUserAgentHeader2[0], value = _getUserAgentHeader2[1];
                    headers = _objectSpread(_defineProperty({}, name, value), this._options.headers);
                    pollOptions = {
                      abortSignal: this._pollAbort.signal,
                      headers: headers,
                      timeout: 100000,
                      withCredentials: this._options.withCredentials
                    };
                    if (transferFormat === _TransferFormat.Binary) {
                      pollOptions.responseType = "arraybuffer";
                    }
                    _context7.next = 13;
                    return this._getAccessToken();
                  case 13:
                    token = _context7.sent;
                    this._updateHeaderToken(pollOptions, token);
                    // Make initial long polling request
                    // Server uses first long polling request to finish initializing connection and it returns without data
                    pollUrl = "".concat(url, "&_=").concat(Date.now());
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) polling: ".concat(pollUrl, "."));
                    _context7.next = 19;
                    return this._httpClient.get(pollUrl, pollOptions);
                  case 19:
                    response = _context7.sent;
                    if (response.statusCode !== 200) {
                      this._logger.log(_LogLevel.Error, "(LongPolling transport) Unexpected response code: ".concat(response.statusCode, "."));
                      // Mark running as false so that the poll immediately ends and runs the close logic
                      this._closeError = new _HttpError(response.statusText || "", response.statusCode);
                      this._running = false;
                    } else {
                      this._running = true;
                    }
                    this._receiving = this._poll(this._url, pollOptions);
                  case 22:
                  case "end":
                    return _context7.stop();
                }
              }, _callee7, this);
            }));
            function connect(_x10, _x11) {
              return _connect.apply(this, arguments);
            }
            return connect;
          }()
        }, {
          key: "_getAccessToken",
          value: function () {
            var _getAccessToken2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee8() {
              return _regeneratorRuntime().wrap(function _callee8$(_context8) {
                while (1) switch (_context8.prev = _context8.next) {
                  case 0:
                    if (!this._accessTokenFactory) {
                      _context8.next = 4;
                      break;
                    }
                    _context8.next = 3;
                    return this._accessTokenFactory();
                  case 3:
                    return _context8.abrupt("return", _context8.sent);
                  case 4:
                    return _context8.abrupt("return", null);
                  case 5:
                  case "end":
                    return _context8.stop();
                }
              }, _callee8, this);
            }));
            function _getAccessToken() {
              return _getAccessToken2.apply(this, arguments);
            }
            return _getAccessToken;
          }()
        }, {
          key: "_updateHeaderToken",
          value: function _updateHeaderToken(request, token) {
            if (!request.headers) {
              request.headers = {};
            }
            if (token) {
              request.headers[HeaderNames.Authorization] = "Bearer ".concat(token);
              return;
            }
            if (request.headers[HeaderNames.Authorization]) {
              delete request.headers[HeaderNames.Authorization];
            }
          }
        }, {
          key: "_poll",
          value: function () {
            var _poll2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee9(url, pollOptions) {
              var token, pollUrl, response;
              return _regeneratorRuntime().wrap(function _callee9$(_context9) {
                while (1) switch (_context9.prev = _context9.next) {
                  case 0:
                    _context9.prev = 0;
                  case 1:
                    if (!this._running) {
                      _context9.next = 20;
                      break;
                    }
                    _context9.next = 4;
                    return this._getAccessToken();
                  case 4:
                    token = _context9.sent;
                    this._updateHeaderToken(pollOptions, token);
                    _context9.prev = 6;
                    pollUrl = "".concat(url, "&_=").concat(Date.now());
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) polling: ".concat(pollUrl, "."));
                    _context9.next = 11;
                    return this._httpClient.get(pollUrl, pollOptions);
                  case 11:
                    response = _context9.sent;
                    if (response.statusCode === 204) {
                      this._logger.log(_LogLevel.Information, "(LongPolling transport) Poll terminated by server.");
                      this._running = false;
                    } else if (response.statusCode !== 200) {
                      this._logger.log(_LogLevel.Error, "(LongPolling transport) Unexpected response code: ".concat(response.statusCode, "."));
                      // Unexpected status code
                      this._closeError = new _HttpError(response.statusText || "", response.statusCode);
                      this._running = false;
                    } else {
                      // Process the response
                      if (response.content) {
                        this._logger.log(_LogLevel.Trace, "(LongPolling transport) data received. ".concat(getDataDetail(response.content, this._options.logMessageContent), "."));
                        if (this.onreceive) {
                          this.onreceive(response.content);
                        }
                      } else {
                        // This is another way timeout manifest.
                        this._logger.log(_LogLevel.Trace, "(LongPolling transport) Poll timed out, reissuing.");
                      }
                    }
                    _context9.next = 18;
                    break;
                  case 15:
                    _context9.prev = 15;
                    _context9.t0 = _context9["catch"](6);
                    if (!this._running) {
                      // Log but disregard errors that occur after stopping
                      this._logger.log(_LogLevel.Trace, "(LongPolling transport) Poll errored after shutdown: ".concat(_context9.t0.message));
                    } else {
                      if (_context9.t0 instanceof _TimeoutError) {
                        // Ignore timeouts and reissue the poll.
                        this._logger.log(_LogLevel.Trace, "(LongPolling transport) Poll timed out, reissuing.");
                      } else {
                        // Close the connection with the error as the result.
                        this._closeError = _context9.t0;
                        this._running = false;
                      }
                    }
                  case 18:
                    _context9.next = 1;
                    break;
                  case 20:
                    _context9.prev = 20;
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) Polling complete.");
                    // We will reach here with pollAborted==false when the server returned a response causing the transport to stop.
                    // If pollAborted==true then client initiated the stop and the stop method will raise the close event after DELETE is sent.
                    if (!this.pollAborted) {
                      this._raiseOnClose();
                    }
                    return _context9.finish(20);
                  case 24:
                  case "end":
                    return _context9.stop();
                }
              }, _callee9, this, [[0,, 20, 24], [6, 15]]);
            }));
            function _poll(_x12, _x13) {
              return _poll2.apply(this, arguments);
            }
            return _poll;
          }()
        }, {
          key: "send",
          value: function () {
            var _send2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee10(data) {
              return _regeneratorRuntime().wrap(function _callee10$(_context10) {
                while (1) switch (_context10.prev = _context10.next) {
                  case 0:
                    if (this._running) {
                      _context10.next = 2;
                      break;
                    }
                    return _context10.abrupt("return", Promise.reject(new Error("Cannot send until the transport is connected")));
                  case 2:
                    return _context10.abrupt("return", sendMessage(this._logger, "LongPolling", this._httpClient, this._url, this._accessTokenFactory, data, this._options));
                  case 3:
                  case "end":
                    return _context10.stop();
                }
              }, _callee10, this);
            }));
            function send(_x14) {
              return _send2.apply(this, arguments);
            }
            return send;
          }()
        }, {
          key: "stop",
          value: function () {
            var _stop2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee11() {
              var headers, _getUserAgentHeader3, _getUserAgentHeader4, name, value, deleteOptions, token;
              return _regeneratorRuntime().wrap(function _callee11$(_context11) {
                while (1) switch (_context11.prev = _context11.next) {
                  case 0:
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) Stopping polling.");
                    // Tell receiving loop to stop, abort any current request, and then wait for it to finish
                    this._running = false;
                    this._pollAbort.abort();
                    _context11.prev = 3;
                    _context11.next = 6;
                    return this._receiving;
                  case 6:
                    // Send DELETE to clean up long polling on the server
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) sending DELETE request to ".concat(this._url, "."));
                    headers = {};
                    _getUserAgentHeader3 = getUserAgentHeader(), _getUserAgentHeader4 = _slicedToArray(_getUserAgentHeader3, 2), name = _getUserAgentHeader4[0], value = _getUserAgentHeader4[1];
                    headers[name] = value;
                    deleteOptions = {
                      headers: _objectSpread(_objectSpread({}, headers), this._options.headers),
                      timeout: this._options.timeout,
                      withCredentials: this._options.withCredentials
                    };
                    _context11.next = 13;
                    return this._getAccessToken();
                  case 13:
                    token = _context11.sent;
                    this._updateHeaderToken(deleteOptions, token);
                    _context11.next = 17;
                    return this._httpClient["delete"](this._url, deleteOptions);
                  case 17:
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) DELETE request sent.");
                  case 18:
                    _context11.prev = 18;
                    this._logger.log(_LogLevel.Trace, "(LongPolling transport) Stop finished.");
                    // Raise close event here instead of in polling
                    // It needs to happen after the DELETE request is sent
                    this._raiseOnClose();
                    return _context11.finish(18);
                  case 22:
                  case "end":
                    return _context11.stop();
                }
              }, _callee11, this, [[3,, 18, 22]]);
            }));
            function stop() {
              return _stop2.apply(this, arguments);
            }
            return stop;
          }()
        }, {
          key: "_raiseOnClose",
          value: function _raiseOnClose() {
            if (this.onclose) {
              var logMessage = "(LongPolling transport) Firing onclose event.";
              if (this._closeError) {
                logMessage += " Error: " + this._closeError;
              }
              this._logger.log(_LogLevel.Trace, logMessage);
              this.onclose(this._closeError);
            }
          }
        }]);
        return LongPollingTransport;
      }();
      ; // CONCATENATED MODULE: ./src/ServerSentEventsTransport.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      /** @private */
      var ServerSentEventsTransport = /*#__PURE__*/function () {
        function ServerSentEventsTransport(httpClient, accessTokenFactory, logger, options) {
          _classCallCheck(this, ServerSentEventsTransport);
          this._httpClient = httpClient;
          this._accessTokenFactory = accessTokenFactory;
          this._logger = logger;
          this._options = options;
          this.onreceive = null;
          this.onclose = null;
        }
        _createClass(ServerSentEventsTransport, [{
          key: "connect",
          value: function () {
            var _connect2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee12(url, transferFormat) {
              var _this24 = this;
              var token;
              return _regeneratorRuntime().wrap(function _callee12$(_context12) {
                while (1) switch (_context12.prev = _context12.next) {
                  case 0:
                    Arg.isRequired(url, "url");
                    Arg.isRequired(transferFormat, "transferFormat");
                    Arg.isIn(transferFormat, _TransferFormat, "transferFormat");
                    this._logger.log(_LogLevel.Trace, "(SSE transport) Connecting.");
                    // set url before accessTokenFactory because this.url is only for send and we set the auth header instead of the query string for send
                    this._url = url;
                    if (!this._accessTokenFactory) {
                      _context12.next = 10;
                      break;
                    }
                    _context12.next = 8;
                    return this._accessTokenFactory();
                  case 8:
                    token = _context12.sent;
                    if (token) {
                      url += (url.indexOf("?") < 0 ? "?" : "&") + "access_token=".concat(encodeURIComponent(token));
                    }
                  case 10:
                    return _context12.abrupt("return", new Promise(function (resolve, reject) {
                      var opened = false;
                      if (transferFormat !== _TransferFormat.Text) {
                        reject(new Error("The Server-Sent Events transport only supports the 'Text' transfer format"));
                        return;
                      }
                      var eventSource;
                      if (Platform.isBrowser || Platform.isWebWorker) {
                        eventSource = new _this24._options.EventSource(url, {
                          withCredentials: _this24._options.withCredentials
                        });
                      } else {
                        // Non-browser passes cookies via the dictionary
                        var cookies = _this24._httpClient.getCookieString(url);
                        var headers = {};
                        headers.Cookie = cookies;
                        var _getUserAgentHeader5 = getUserAgentHeader(),
                          _getUserAgentHeader6 = _slicedToArray(_getUserAgentHeader5, 2),
                          name = _getUserAgentHeader6[0],
                          value = _getUserAgentHeader6[1];
                        headers[name] = value;
                        eventSource = new _this24._options.EventSource(url, {
                          withCredentials: _this24._options.withCredentials,
                          headers: _objectSpread(_objectSpread({}, headers), _this24._options.headers)
                        });
                      }
                      try {
                        eventSource.onmessage = function (e) {
                          if (_this24.onreceive) {
                            try {
                              _this24._logger.log(_LogLevel.Trace, "(SSE transport) data received. ".concat(getDataDetail(e.data, _this24._options.logMessageContent), "."));
                              _this24.onreceive(e.data);
                            } catch (error) {
                              _this24._close(error);
                              return;
                            }
                          }
                        };
                        // @ts-ignore: not using event on purpose
                        eventSource.onerror = function (e) {
                          // EventSource doesn't give any useful information about server side closes.
                          if (opened) {
                            _this24._close();
                          } else {
                            reject(new Error("EventSource failed to connect. The connection could not be found on the server," + " either the connection ID is not present on the server, or a proxy is refusing/buffering the connection." + " If you have multiple servers check that sticky sessions are enabled."));
                          }
                        };
                        eventSource.onopen = function () {
                          _this24._logger.log(_LogLevel.Information, "SSE connected to ".concat(_this24._url));
                          _this24._eventSource = eventSource;
                          opened = true;
                          resolve();
                        };
                      } catch (e) {
                        reject(e);
                        return;
                      }
                    }));
                  case 11:
                  case "end":
                    return _context12.stop();
                }
              }, _callee12, this);
            }));
            function connect(_x15, _x16) {
              return _connect2.apply(this, arguments);
            }
            return connect;
          }()
        }, {
          key: "send",
          value: function () {
            var _send3 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee13(data) {
              return _regeneratorRuntime().wrap(function _callee13$(_context13) {
                while (1) switch (_context13.prev = _context13.next) {
                  case 0:
                    if (this._eventSource) {
                      _context13.next = 2;
                      break;
                    }
                    return _context13.abrupt("return", Promise.reject(new Error("Cannot send until the transport is connected")));
                  case 2:
                    return _context13.abrupt("return", sendMessage(this._logger, "SSE", this._httpClient, this._url, this._accessTokenFactory, data, this._options));
                  case 3:
                  case "end":
                    return _context13.stop();
                }
              }, _callee13, this);
            }));
            function send(_x17) {
              return _send3.apply(this, arguments);
            }
            return send;
          }()
        }, {
          key: "stop",
          value: function stop() {
            this._close();
            return Promise.resolve();
          }
        }, {
          key: "_close",
          value: function _close(e) {
            if (this._eventSource) {
              this._eventSource.close();
              this._eventSource = undefined;
              if (this.onclose) {
                this.onclose(e);
              }
            }
          }
        }]);
        return ServerSentEventsTransport;
      }();
      ; // CONCATENATED MODULE: ./src/WebSocketTransport.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      /** @private */
      var WebSocketTransport = /*#__PURE__*/function () {
        function WebSocketTransport(httpClient, accessTokenFactory, logger, logMessageContent, webSocketConstructor, headers) {
          _classCallCheck(this, WebSocketTransport);
          this._logger = logger;
          this._accessTokenFactory = accessTokenFactory;
          this._logMessageContent = logMessageContent;
          this._webSocketConstructor = webSocketConstructor;
          this._httpClient = httpClient;
          this.onreceive = null;
          this.onclose = null;
          this._headers = headers;
        }
        _createClass(WebSocketTransport, [{
          key: "connect",
          value: function () {
            var _connect3 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee14(url, transferFormat) {
              var _this25 = this;
              var token;
              return _regeneratorRuntime().wrap(function _callee14$(_context14) {
                while (1) switch (_context14.prev = _context14.next) {
                  case 0:
                    Arg.isRequired(url, "url");
                    Arg.isRequired(transferFormat, "transferFormat");
                    Arg.isIn(transferFormat, _TransferFormat, "transferFormat");
                    this._logger.log(_LogLevel.Trace, "(WebSockets transport) Connecting.");
                    if (!this._accessTokenFactory) {
                      _context14.next = 9;
                      break;
                    }
                    _context14.next = 7;
                    return this._accessTokenFactory();
                  case 7:
                    token = _context14.sent;
                    if (token) {
                      url += (url.indexOf("?") < 0 ? "?" : "&") + "access_token=".concat(encodeURIComponent(token));
                    }
                  case 9:
                    return _context14.abrupt("return", new Promise(function (resolve, reject) {
                      url = url.replace(/^http/, "ws");
                      var webSocket;
                      var cookies = _this25._httpClient.getCookieString(url);
                      var opened = false;
                      if (Platform.isNode) {
                        var headers = {};
                        var _getUserAgentHeader7 = getUserAgentHeader(),
                          _getUserAgentHeader8 = _slicedToArray(_getUserAgentHeader7, 2),
                          name = _getUserAgentHeader8[0],
                          value = _getUserAgentHeader8[1];
                        headers[name] = value;
                        if (cookies) {
                          headers[HeaderNames.Cookie] = "".concat(cookies);
                        }
                        // Only pass headers when in non-browser environments
                        webSocket = new _this25._webSocketConstructor(url, undefined, {
                          headers: _objectSpread(_objectSpread({}, headers), _this25._headers)
                        });
                      }
                      if (!webSocket) {
                        // Chrome is not happy with passing 'undefined' as protocol
                        webSocket = new _this25._webSocketConstructor(url);
                      }
                      if (transferFormat === _TransferFormat.Binary) {
                        webSocket.binaryType = "arraybuffer";
                      }
                      webSocket.onopen = function (_event) {
                        _this25._logger.log(_LogLevel.Information, "WebSocket connected to ".concat(url, "."));
                        _this25._webSocket = webSocket;
                        opened = true;
                        resolve();
                      };
                      webSocket.onerror = function (event) {
                        var error = null;
                        // ErrorEvent is a browser only type we need to check if the type exists before using it
                        if (typeof ErrorEvent !== "undefined" && event instanceof ErrorEvent) {
                          error = event.error;
                        } else {
                          error = "There was an error with the transport";
                        }
                        _this25._logger.log(_LogLevel.Information, "(WebSockets transport) ".concat(error, "."));
                      };
                      webSocket.onmessage = function (message) {
                        _this25._logger.log(_LogLevel.Trace, "(WebSockets transport) data received. ".concat(getDataDetail(message.data, _this25._logMessageContent), "."));
                        if (_this25.onreceive) {
                          try {
                            _this25.onreceive(message.data);
                          } catch (error) {
                            _this25._close(error);
                            return;
                          }
                        }
                      };
                      webSocket.onclose = function (event) {
                        // Don't call close handler if connection was never established
                        // We'll reject the connect call instead
                        if (opened) {
                          _this25._close(event);
                        } else {
                          var error = null;
                          // ErrorEvent is a browser only type we need to check if the type exists before using it
                          if (typeof ErrorEvent !== "undefined" && event instanceof ErrorEvent) {
                            error = event.error;
                          } else {
                            error = "WebSocket failed to connect. The connection could not be found on the server," + " either the endpoint may not be a SignalR endpoint," + " the connection ID is not present on the server, or there is a proxy blocking WebSockets." + " If you have multiple servers check that sticky sessions are enabled.";
                          }
                          reject(new Error(error));
                        }
                      };
                    }));
                  case 10:
                  case "end":
                    return _context14.stop();
                }
              }, _callee14, this);
            }));
            function connect(_x18, _x19) {
              return _connect3.apply(this, arguments);
            }
            return connect;
          }()
        }, {
          key: "send",
          value: function send(data) {
            if (this._webSocket && this._webSocket.readyState === this._webSocketConstructor.OPEN) {
              this._logger.log(_LogLevel.Trace, "(WebSockets transport) sending data. ".concat(getDataDetail(data, this._logMessageContent), "."));
              this._webSocket.send(data);
              return Promise.resolve();
            }
            return Promise.reject("WebSocket is not in the OPEN state");
          }
        }, {
          key: "stop",
          value: function stop() {
            if (this._webSocket) {
              // Manually invoke onclose callback inline so we know the HttpConnection was closed properly before returning
              // This also solves an issue where websocket.onclose could take 18+ seconds to trigger during network disconnects
              this._close(undefined);
            }
            return Promise.resolve();
          }
        }, {
          key: "_close",
          value: function _close(event) {
            // webSocket will be null if the transport did not start successfully
            if (this._webSocket) {
              // Clear websocket handlers because we are considering the socket closed now
              this._webSocket.onclose = function () {};
              this._webSocket.onmessage = function () {};
              this._webSocket.onerror = function () {};
              this._webSocket.close();
              this._webSocket = undefined;
            }
            this._logger.log(_LogLevel.Trace, "(WebSockets transport) socket closed.");
            if (this.onclose) {
              if (this._isCloseEvent(event) && (event.wasClean === false || event.code !== 1000)) {
                this.onclose(new Error("WebSocket closed with status code: ".concat(event.code, " (").concat(event.reason || "no reason given", ").")));
              } else if (event instanceof Error) {
                this.onclose(event);
              } else {
                this.onclose();
              }
            }
          }
        }, {
          key: "_isCloseEvent",
          value: function _isCloseEvent(event) {
            return event && typeof event.wasClean === "boolean" && typeof event.code === "number";
          }
        }]);
        return WebSocketTransport;
      }();
      ; // CONCATENATED MODULE: ./src/HttpConnection.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      var MAX_REDIRECTS = 100;
      /** @private */
      var HttpConnection = /*#__PURE__*/function () {
        function HttpConnection(url) {
          var options = arguments.length > 1 && arguments[1] !== undefined ? arguments[1] : {};
          _classCallCheck(this, HttpConnection);
          this._stopPromiseResolver = function () {};
          this.features = {};
          this._negotiateVersion = 1;
          Arg.isRequired(url, "url");
          this._logger = createLogger(options.logger);
          this.baseUrl = this._resolveUrl(url);
          options = options || {};
          options.logMessageContent = options.logMessageContent === undefined ? false : options.logMessageContent;
          if (typeof options.withCredentials === "boolean" || options.withCredentials === undefined) {
            options.withCredentials = options.withCredentials === undefined ? true : options.withCredentials;
          } else {
            throw new Error("withCredentials option was not a 'boolean' or 'undefined' value");
          }
          options.timeout = options.timeout === undefined ? 100 * 1000 : options.timeout;
          var webSocketModule = null;
          var eventSourceModule = null;
          if (Platform.isNode && "function" !== "undefined") {
            // In order to ignore the dynamic require in webpack builds we need to do this magic
            // @ts-ignore: TS doesn't know about these names
            var requireFunc = true ? require : 0;
            webSocketModule = requireFunc("ws");
            eventSourceModule = requireFunc("eventsource");
          }
          if (!Platform.isNode && typeof WebSocket !== "undefined" && !options.WebSocket) {
            options.WebSocket = WebSocket;
          } else if (Platform.isNode && !options.WebSocket) {
            if (webSocketModule) {
              options.WebSocket = webSocketModule;
            }
          }
          if (!Platform.isNode && typeof EventSource !== "undefined" && !options.EventSource) {
            options.EventSource = EventSource;
          } else if (Platform.isNode && !options.EventSource) {
            if (typeof eventSourceModule !== "undefined") {
              options.EventSource = eventSourceModule;
            }
          }
          this._httpClient = options.httpClient || new _DefaultHttpClient(this._logger);
          this._connectionState = "Disconnected" /* Disconnected */;
          this._connectionStarted = false;
          this._options = options;
          this.onreceive = null;
          this.onclose = null;
        }
        _createClass(HttpConnection, [{
          key: "start",
          value: function () {
            var _start = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee15(transferFormat) {
              var message, _message3;
              return _regeneratorRuntime().wrap(function _callee15$(_context15) {
                while (1) switch (_context15.prev = _context15.next) {
                  case 0:
                    transferFormat = transferFormat || _TransferFormat.Binary;
                    Arg.isIn(transferFormat, _TransferFormat, "transferFormat");
                    this._logger.log(_LogLevel.Debug, "Starting connection with transfer format '".concat(_TransferFormat[transferFormat], "'."));
                    if (!(this._connectionState !== "Disconnected" /* Disconnected */)) {
                      _context15.next = 5;
                      break;
                    }
                    return _context15.abrupt("return", Promise.reject(new Error("Cannot start an HttpConnection that is not in the 'Disconnected' state.")));
                  case 5:
                    this._connectionState = "Connecting" /* Connecting */;
                    this._startInternalPromise = this._startInternal(transferFormat);
                    _context15.next = 9;
                    return this._startInternalPromise;
                  case 9:
                    if (!(this._connectionState === "Disconnecting" /* Disconnecting */)) {
                      _context15.next = 17;
                      break;
                    }
                    // stop() was called and transitioned the client into the Disconnecting state.
                    message = "Failed to start the HttpConnection before stop() was called.";
                    this._logger.log(_LogLevel.Error, message);
                    // We cannot await stopPromise inside startInternal since stopInternal awaits the startInternalPromise.
                    _context15.next = 14;
                    return this._stopPromise;
                  case 14:
                    return _context15.abrupt("return", Promise.reject(new Error(message)));
                  case 17:
                    if (!(this._connectionState !== "Connected" /* Connected */)) {
                      _context15.next = 21;
                      break;
                    }
                    // stop() was called and transitioned the client into the Disconnecting state.
                    _message3 = "HttpConnection.startInternal completed gracefully but didn't enter the connection into the connected state!";
                    this._logger.log(_LogLevel.Error, _message3);
                    return _context15.abrupt("return", Promise.reject(new Error(_message3)));
                  case 21:
                    this._connectionStarted = true;
                  case 22:
                  case "end":
                    return _context15.stop();
                }
              }, _callee15, this);
            }));
            function start(_x20) {
              return _start.apply(this, arguments);
            }
            return start;
          }()
        }, {
          key: "send",
          value: function send(data) {
            if (this._connectionState !== "Connected" /* Connected */) {
              return Promise.reject(new Error("Cannot send data if the connection is not in the 'Connected' State."));
            }
            if (!this._sendQueue) {
              this._sendQueue = new TransportSendQueue(this.transport);
            }
            // Transport will not be null if state is connected
            return this._sendQueue.send(data);
          }
        }, {
          key: "stop",
          value: function () {
            var _stop3 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee16(error) {
              var _this26 = this;
              return _regeneratorRuntime().wrap(function _callee16$(_context16) {
                while (1) switch (_context16.prev = _context16.next) {
                  case 0:
                    if (!(this._connectionState === "Disconnected" /* Disconnected */)) {
                      _context16.next = 3;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Call to HttpConnection.stop(".concat(error, ") ignored because the connection is already in the disconnected state."));
                    return _context16.abrupt("return", Promise.resolve());
                  case 3:
                    if (!(this._connectionState === "Disconnecting" /* Disconnecting */)) {
                      _context16.next = 6;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Call to HttpConnection.stop(".concat(error, ") ignored because the connection is already in the disconnecting state."));
                    return _context16.abrupt("return", this._stopPromise);
                  case 6:
                    this._connectionState = "Disconnecting" /* Disconnecting */;
                    this._stopPromise = new Promise(function (resolve) {
                      // Don't complete stop() until stopConnection() completes.
                      _this26._stopPromiseResolver = resolve;
                    });
                    // stopInternal should never throw so just observe it.
                    _context16.next = 10;
                    return this._stopInternal(error);
                  case 10:
                    _context16.next = 12;
                    return this._stopPromise;
                  case 12:
                  case "end":
                    return _context16.stop();
                }
              }, _callee16, this);
            }));
            function stop(_x21) {
              return _stop3.apply(this, arguments);
            }
            return stop;
          }()
        }, {
          key: "_stopInternal",
          value: function () {
            var _stopInternal2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee17(error) {
              return _regeneratorRuntime().wrap(function _callee17$(_context17) {
                while (1) switch (_context17.prev = _context17.next) {
                  case 0:
                    // Set error as soon as possible otherwise there is a race between
                    // the transport closing and providing an error and the error from a close message
                    // We would prefer the close message error.
                    this._stopError = error;
                    _context17.prev = 1;
                    _context17.next = 4;
                    return this._startInternalPromise;
                  case 4:
                    _context17.next = 8;
                    break;
                  case 6:
                    _context17.prev = 6;
                    _context17.t0 = _context17["catch"](1);
                  case 8:
                    if (!this.transport) {
                      _context17.next = 21;
                      break;
                    }
                    _context17.prev = 9;
                    _context17.next = 12;
                    return this.transport.stop();
                  case 12:
                    _context17.next = 18;
                    break;
                  case 14:
                    _context17.prev = 14;
                    _context17.t1 = _context17["catch"](9);
                    this._logger.log(_LogLevel.Error, "HttpConnection.transport.stop() threw error '".concat(_context17.t1, "'."));
                    this._stopConnection();
                  case 18:
                    this.transport = undefined;
                    _context17.next = 22;
                    break;
                  case 21:
                    this._logger.log(_LogLevel.Debug, "HttpConnection.transport is undefined in HttpConnection.stop() because start() failed.");
                  case 22:
                  case "end":
                    return _context17.stop();
                }
              }, _callee17, this, [[1, 6], [9, 14]]);
            }));
            function _stopInternal(_x22) {
              return _stopInternal2.apply(this, arguments);
            }
            return _stopInternal;
          }()
        }, {
          key: "_startInternal",
          value: function () {
            var _startInternal3 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee18(transferFormat) {
              var _this27 = this;
              var url, negotiateResponse, redirects, _loop2;
              return _regeneratorRuntime().wrap(function _callee18$(_context19) {
                while (1) switch (_context19.prev = _context19.next) {
                  case 0:
                    // Store the original base url and the access token factory since they may change
                    // as part of negotiating
                    url = this.baseUrl;
                    this._accessTokenFactory = this._options.accessTokenFactory;
                    _context19.prev = 2;
                    if (!this._options.skipNegotiation) {
                      _context19.next = 13;
                      break;
                    }
                    if (!(this._options.transport === _HttpTransportType.WebSockets)) {
                      _context19.next = 10;
                      break;
                    }
                    // No need to add a connection ID in this case
                    this.transport = this._constructTransport(_HttpTransportType.WebSockets);
                    // We should just call connect directly in this case.
                    // No fallback or negotiate in this case.
                    _context19.next = 8;
                    return this._startTransport(url, transferFormat);
                  case 8:
                    _context19.next = 11;
                    break;
                  case 10:
                    throw new Error("Negotiation can only be skipped when using the WebSocket transport directly.");
                  case 11:
                    _context19.next = 22;
                    break;
                  case 13:
                    negotiateResponse = null;
                    redirects = 0;
                    _loop2 = /*#__PURE__*/_regeneratorRuntime().mark(function _loop2() {
                      var accessToken;
                      return _regeneratorRuntime().wrap(function _loop2$(_context18) {
                        while (1) switch (_context18.prev = _context18.next) {
                          case 0:
                            _context18.next = 2;
                            return _this27._getNegotiationResponse(url);
                          case 2:
                            negotiateResponse = _context18.sent;
                            if (!(_this27._connectionState === "Disconnecting" /* Disconnecting */ || _this27._connectionState === "Disconnected" /* Disconnected */)) {
                              _context18.next = 5;
                              break;
                            }
                            throw new Error("The connection was stopped during negotiation.");
                          case 5:
                            if (!negotiateResponse.error) {
                              _context18.next = 7;
                              break;
                            }
                            throw new Error(negotiateResponse.error);
                          case 7:
                            if (!negotiateResponse.ProtocolVersion) {
                              _context18.next = 9;
                              break;
                            }
                            throw new Error("Detected a connection attempt to an ASP.NET SignalR Server. This client only supports connecting to an ASP.NET Core SignalR Server. See https://aka.ms/signalr-core-differences for details.");
                          case 9:
                            if (negotiateResponse.url) {
                              url = negotiateResponse.url;
                            }
                            if (negotiateResponse.accessToken) {
                              // Replace the current access token factory with one that uses
                              // the returned access token
                              accessToken = negotiateResponse.accessToken;
                              _this27._accessTokenFactory = function () {
                                return accessToken;
                              };
                            }
                            redirects++;
                          case 12:
                          case "end":
                            return _context18.stop();
                        }
                      }, _loop2);
                    });
                  case 16:
                    return _context19.delegateYield(_loop2(), "t0", 17);
                  case 17:
                    if (negotiateResponse.url && redirects < MAX_REDIRECTS) {
                      _context19.next = 16;
                      break;
                    }
                  case 18:
                    if (!(redirects === MAX_REDIRECTS && negotiateResponse.url)) {
                      _context19.next = 20;
                      break;
                    }
                    throw new Error("Negotiate redirection limit exceeded.");
                  case 20:
                    _context19.next = 22;
                    return this._createTransport(url, this._options.transport, negotiateResponse, transferFormat);
                  case 22:
                    if (this.transport instanceof LongPollingTransport) {
                      this.features.inherentKeepAlive = true;
                    }
                    if (this._connectionState === "Connecting" /* Connecting */) {
                      // Ensure the connection transitions to the connected state prior to completing this.startInternalPromise.
                      // start() will handle the case when stop was called and startInternal exits still in the disconnecting state.
                      this._logger.log(_LogLevel.Debug, "The HttpConnection connected successfully.");
                      this._connectionState = "Connected" /* Connected */;
                    }
                    // stop() is waiting on us via this.startInternalPromise so keep this.transport around so it can clean up.
                    // This is the only case startInternal can exit in neither the connected nor disconnected state because stopConnection()
                    // will transition to the disconnected state. start() will wait for the transition using the stopPromise.
                    _context19.next = 33;
                    break;
                  case 26:
                    _context19.prev = 26;
                    _context19.t1 = _context19["catch"](2);
                    this._logger.log(_LogLevel.Error, "Failed to start the connection: " + _context19.t1);
                    this._connectionState = "Disconnected" /* Disconnected */;
                    this.transport = undefined;
                    // if start fails, any active calls to stop assume that start will complete the stop promise
                    this._stopPromiseResolver();
                    return _context19.abrupt("return", Promise.reject(_context19.t1));
                  case 33:
                  case "end":
                    return _context19.stop();
                }
              }, _callee18, this, [[2, 26]]);
            }));
            function _startInternal(_x23) {
              return _startInternal3.apply(this, arguments);
            }
            return _startInternal;
          }()
        }, {
          key: "_getNegotiationResponse",
          value: function () {
            var _getNegotiationResponse2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee19(url) {
              var headers, token, _getUserAgentHeader9, _getUserAgentHeader10, name, value, negotiateUrl, response, negotiateResponse, errorMessage;
              return _regeneratorRuntime().wrap(function _callee19$(_context20) {
                while (1) switch (_context20.prev = _context20.next) {
                  case 0:
                    headers = {};
                    if (!this._accessTokenFactory) {
                      _context20.next = 6;
                      break;
                    }
                    _context20.next = 4;
                    return this._accessTokenFactory();
                  case 4:
                    token = _context20.sent;
                    if (token) {
                      headers[HeaderNames.Authorization] = "Bearer ".concat(token);
                    }
                  case 6:
                    _getUserAgentHeader9 = getUserAgentHeader(), _getUserAgentHeader10 = _slicedToArray(_getUserAgentHeader9, 2), name = _getUserAgentHeader10[0], value = _getUserAgentHeader10[1];
                    headers[name] = value;
                    negotiateUrl = this._resolveNegotiateUrl(url);
                    this._logger.log(_LogLevel.Debug, "Sending negotiation request: ".concat(negotiateUrl, "."));
                    _context20.prev = 10;
                    _context20.next = 13;
                    return this._httpClient.post(negotiateUrl, {
                      content: "",
                      headers: _objectSpread(_objectSpread({}, headers), this._options.headers),
                      timeout: this._options.timeout,
                      withCredentials: this._options.withCredentials
                    });
                  case 13:
                    response = _context20.sent;
                    if (!(response.statusCode !== 200)) {
                      _context20.next = 16;
                      break;
                    }
                    return _context20.abrupt("return", Promise.reject(new Error("Unexpected status code returned from negotiate '".concat(response.statusCode, "'"))));
                  case 16:
                    negotiateResponse = JSON.parse(response.content);
                    if (!negotiateResponse.negotiateVersion || negotiateResponse.negotiateVersion < 1) {
                      // Negotiate version 0 doesn't use connectionToken
                      // So we set it equal to connectionId so all our logic can use connectionToken without being aware of the negotiate version
                      negotiateResponse.connectionToken = negotiateResponse.connectionId;
                    }
                    return _context20.abrupt("return", negotiateResponse);
                  case 21:
                    _context20.prev = 21;
                    _context20.t0 = _context20["catch"](10);
                    errorMessage = "Failed to complete negotiation with the server: " + _context20.t0;
                    if (_context20.t0 instanceof _HttpError) {
                      if (_context20.t0.statusCode === 404) {
                        errorMessage = errorMessage + " Either this is not a SignalR endpoint or there is a proxy blocking the connection.";
                      }
                    }
                    this._logger.log(_LogLevel.Error, errorMessage);
                    return _context20.abrupt("return", Promise.reject(new FailedToNegotiateWithServerError(errorMessage)));
                  case 27:
                  case "end":
                    return _context20.stop();
                }
              }, _callee19, this, [[10, 21]]);
            }));
            function _getNegotiationResponse(_x24) {
              return _getNegotiationResponse2.apply(this, arguments);
            }
            return _getNegotiationResponse;
          }()
        }, {
          key: "_createConnectUrl",
          value: function _createConnectUrl(url, connectionToken) {
            if (!connectionToken) {
              return url;
            }
            return url + (url.indexOf("?") === -1 ? "?" : "&") + "id=".concat(connectionToken);
          }
        }, {
          key: "_createTransport",
          value: function () {
            var _createTransport2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee20(url, requestedTransport, negotiateResponse, requestedTransferFormat) {
              var connectUrl, transportExceptions, transports, negotiate, _iterator5, _step5, endpoint, transportOrError, message;
              return _regeneratorRuntime().wrap(function _callee20$(_context21) {
                while (1) switch (_context21.prev = _context21.next) {
                  case 0:
                    connectUrl = this._createConnectUrl(url, negotiateResponse.connectionToken);
                    if (!this._isITransport(requestedTransport)) {
                      _context21.next = 8;
                      break;
                    }
                    this._logger.log(_LogLevel.Debug, "Connection was provided an instance of ITransport, using that directly.");
                    this.transport = requestedTransport;
                    _context21.next = 6;
                    return this._startTransport(connectUrl, requestedTransferFormat);
                  case 6:
                    this.connectionId = negotiateResponse.connectionId;
                    return _context21.abrupt("return");
                  case 8:
                    transportExceptions = [];
                    transports = negotiateResponse.availableTransports || [];
                    negotiate = negotiateResponse;
                    _iterator5 = _createForOfIteratorHelper(transports);
                    _context21.prev = 12;
                    _iterator5.s();
                  case 14:
                    if ((_step5 = _iterator5.n()).done) {
                      _context21.next = 53;
                      break;
                    }
                    endpoint = _step5.value;
                    transportOrError = this._resolveTransportOrError(endpoint, requestedTransport, requestedTransferFormat);
                    if (!(transportOrError instanceof Error)) {
                      _context21.next = 22;
                      break;
                    }
                    // Store the error and continue, we don't want to cause a re-negotiate in these cases
                    transportExceptions.push("".concat(endpoint.transport, " failed:"));
                    transportExceptions.push(transportOrError);
                    _context21.next = 51;
                    break;
                  case 22:
                    if (!this._isITransport(transportOrError)) {
                      _context21.next = 51;
                      break;
                    }
                    this.transport = transportOrError;
                    if (negotiate) {
                      _context21.next = 35;
                      break;
                    }
                    _context21.prev = 25;
                    _context21.next = 28;
                    return this._getNegotiationResponse(url);
                  case 28:
                    negotiate = _context21.sent;
                    _context21.next = 34;
                    break;
                  case 31:
                    _context21.prev = 31;
                    _context21.t0 = _context21["catch"](25);
                    return _context21.abrupt("return", Promise.reject(_context21.t0));
                  case 34:
                    connectUrl = this._createConnectUrl(url, negotiate.connectionToken);
                  case 35:
                    _context21.prev = 35;
                    _context21.next = 38;
                    return this._startTransport(connectUrl, requestedTransferFormat);
                  case 38:
                    this.connectionId = negotiate.connectionId;
                    return _context21.abrupt("return");
                  case 42:
                    _context21.prev = 42;
                    _context21.t1 = _context21["catch"](35);
                    this._logger.log(_LogLevel.Error, "Failed to start the transport '".concat(endpoint.transport, "': ").concat(_context21.t1));
                    negotiate = undefined;
                    transportExceptions.push(new FailedToStartTransportError("".concat(endpoint.transport, " failed: ").concat(_context21.t1), _HttpTransportType[endpoint.transport]));
                    if (!(this._connectionState !== "Connecting" /* Connecting */)) {
                      _context21.next = 51;
                      break;
                    }
                    message = "Failed to select transport before stop() was called.";
                    this._logger.log(_LogLevel.Debug, message);
                    return _context21.abrupt("return", Promise.reject(new Error(message)));
                  case 51:
                    _context21.next = 14;
                    break;
                  case 53:
                    _context21.next = 58;
                    break;
                  case 55:
                    _context21.prev = 55;
                    _context21.t2 = _context21["catch"](12);
                    _iterator5.e(_context21.t2);
                  case 58:
                    _context21.prev = 58;
                    _iterator5.f();
                    return _context21.finish(58);
                  case 61:
                    if (!(transportExceptions.length > 0)) {
                      _context21.next = 63;
                      break;
                    }
                    return _context21.abrupt("return", Promise.reject(new AggregateErrors("Unable to connect to the server with any of the available transports. ".concat(transportExceptions.join(" ")), transportExceptions)));
                  case 63:
                    return _context21.abrupt("return", Promise.reject(new Error("None of the transports supported by the client are supported by the server.")));
                  case 64:
                  case "end":
                    return _context21.stop();
                }
              }, _callee20, this, [[12, 55, 58, 61], [25, 31], [35, 42]]);
            }));
            function _createTransport(_x25, _x26, _x27, _x28) {
              return _createTransport2.apply(this, arguments);
            }
            return _createTransport;
          }()
        }, {
          key: "_constructTransport",
          value: function _constructTransport(transport) {
            switch (transport) {
              case _HttpTransportType.WebSockets:
                if (!this._options.WebSocket) {
                  throw new Error("'WebSocket' is not supported in your environment.");
                }
                return new WebSocketTransport(this._httpClient, this._accessTokenFactory, this._logger, this._options.logMessageContent, this._options.WebSocket, this._options.headers || {});
              case _HttpTransportType.ServerSentEvents:
                if (!this._options.EventSource) {
                  throw new Error("'EventSource' is not supported in your environment.");
                }
                return new ServerSentEventsTransport(this._httpClient, this._accessTokenFactory, this._logger, this._options);
              case _HttpTransportType.LongPolling:
                return new LongPollingTransport(this._httpClient, this._accessTokenFactory, this._logger, this._options);
              default:
                throw new Error("Unknown transport: ".concat(transport, "."));
            }
          }
        }, {
          key: "_startTransport",
          value: function _startTransport(url, transferFormat) {
            var _this28 = this;
            this.transport.onreceive = this.onreceive;
            this.transport.onclose = function (e) {
              return _this28._stopConnection(e);
            };
            return this.transport.connect(url, transferFormat);
          }
        }, {
          key: "_resolveTransportOrError",
          value: function _resolveTransportOrError(endpoint, requestedTransport, requestedTransferFormat) {
            var transport = _HttpTransportType[endpoint.transport];
            if (transport === null || transport === undefined) {
              this._logger.log(_LogLevel.Debug, "Skipping transport '".concat(endpoint.transport, "' because it is not supported by this client."));
              return new Error("Skipping transport '".concat(endpoint.transport, "' because it is not supported by this client."));
            } else {
              if (transportMatches(requestedTransport, transport)) {
                var transferFormats = endpoint.transferFormats.map(function (s) {
                  return _TransferFormat[s];
                });
                if (transferFormats.indexOf(requestedTransferFormat) >= 0) {
                  if (transport === _HttpTransportType.WebSockets && !this._options.WebSocket || transport === _HttpTransportType.ServerSentEvents && !this._options.EventSource) {
                    this._logger.log(_LogLevel.Debug, "Skipping transport '".concat(_HttpTransportType[transport], "' because it is not supported in your environment.'"));
                    return new UnsupportedTransportError("'".concat(_HttpTransportType[transport], "' is not supported in your environment."), transport);
                  } else {
                    this._logger.log(_LogLevel.Debug, "Selecting transport '".concat(_HttpTransportType[transport], "'."));
                    try {
                      return this._constructTransport(transport);
                    } catch (ex) {
                      return ex;
                    }
                  }
                } else {
                  this._logger.log(_LogLevel.Debug, "Skipping transport '".concat(_HttpTransportType[transport], "' because it does not support the requested transfer format '").concat(_TransferFormat[requestedTransferFormat], "'."));
                  return new Error("'".concat(_HttpTransportType[transport], "' does not support ").concat(_TransferFormat[requestedTransferFormat], "."));
                }
              } else {
                this._logger.log(_LogLevel.Debug, "Skipping transport '".concat(_HttpTransportType[transport], "' because it was disabled by the client."));
                return new DisabledTransportError("'".concat(_HttpTransportType[transport], "' is disabled by the client."), transport);
              }
            }
          }
        }, {
          key: "_isITransport",
          value: function _isITransport(transport) {
            return transport && _typeof(transport) === "object" && "connect" in transport;
          }
        }, {
          key: "_stopConnection",
          value: function _stopConnection(error) {
            var _this29 = this;
            this._logger.log(_LogLevel.Debug, "HttpConnection.stopConnection(".concat(error, ") called while in state ").concat(this._connectionState, "."));
            this.transport = undefined;
            // If we have a stopError, it takes precedence over the error from the transport
            error = this._stopError || error;
            this._stopError = undefined;
            if (this._connectionState === "Disconnected" /* Disconnected */) {
              this._logger.log(_LogLevel.Debug, "Call to HttpConnection.stopConnection(".concat(error, ") was ignored because the connection is already in the disconnected state."));
              return;
            }
            if (this._connectionState === "Connecting" /* Connecting */) {
              this._logger.log(_LogLevel.Warning, "Call to HttpConnection.stopConnection(".concat(error, ") was ignored because the connection is still in the connecting state."));
              throw new Error("HttpConnection.stopConnection(".concat(error, ") was called while the connection is still in the connecting state."));
            }
            if (this._connectionState === "Disconnecting" /* Disconnecting */) {
              // A call to stop() induced this call to stopConnection and needs to be completed.
              // Any stop() awaiters will be scheduled to continue after the onclose callback fires.
              this._stopPromiseResolver();
            }
            if (error) {
              this._logger.log(_LogLevel.Error, "Connection disconnected with error '".concat(error, "'."));
            } else {
              this._logger.log(_LogLevel.Information, "Connection disconnected.");
            }
            if (this._sendQueue) {
              this._sendQueue.stop()["catch"](function (e) {
                _this29._logger.log(_LogLevel.Error, "TransportSendQueue.stop() threw error '".concat(e, "'."));
              });
              this._sendQueue = undefined;
            }
            this.connectionId = undefined;
            this._connectionState = "Disconnected" /* Disconnected */;
            if (this._connectionStarted) {
              this._connectionStarted = false;
              try {
                if (this.onclose) {
                  this.onclose(error);
                }
              } catch (e) {
                this._logger.log(_LogLevel.Error, "HttpConnection.onclose(".concat(error, ") threw error '").concat(e, "'."));
              }
            }
          }
        }, {
          key: "_resolveUrl",
          value: function _resolveUrl(url) {
            // startsWith is not supported in IE
            if (url.lastIndexOf("https://", 0) === 0 || url.lastIndexOf("http://", 0) === 0) {
              return url;
            }
            if (!Platform.isBrowser) {
              throw new Error("Cannot resolve '".concat(url, "'."));
            }
            // Setting the url to the href propery of an anchor tag handles normalization
            // for us. There are 3 main cases.
            // 1. Relative path normalization e.g "b" -> "http://localhost:5000/a/b"
            // 2. Absolute path normalization e.g "/a/b" -> "http://localhost:5000/a/b"
            // 3. Networkpath reference normalization e.g "//localhost:5000/a/b" -> "http://localhost:5000/a/b"
            var aTag = window.document.createElement("a");
            aTag.href = url;
            this._logger.log(_LogLevel.Information, "Normalizing '".concat(url, "' to '").concat(aTag.href, "'."));
            return aTag.href;
          }
        }, {
          key: "_resolveNegotiateUrl",
          value: function _resolveNegotiateUrl(url) {
            var index = url.indexOf("?");
            var negotiateUrl = url.substring(0, index === -1 ? url.length : index);
            if (negotiateUrl[negotiateUrl.length - 1] !== "/") {
              negotiateUrl += "/";
            }
            negotiateUrl += "negotiate";
            negotiateUrl += index === -1 ? "" : url.substring(index);
            if (negotiateUrl.indexOf("negotiateVersion") === -1) {
              negotiateUrl += index === -1 ? "?" : "&";
              negotiateUrl += "negotiateVersion=" + this._negotiateVersion;
            }
            return negotiateUrl;
          }
        }]);
        return HttpConnection;
      }();
      function transportMatches(requestedTransport, actualTransport) {
        return !requestedTransport || (actualTransport & requestedTransport) !== 0;
      }
      /** @private */
      var TransportSendQueue = /*#__PURE__*/function () {
        function TransportSendQueue(_transport) {
          _classCallCheck(this, TransportSendQueue);
          this._transport = _transport;
          this._buffer = [];
          this._executing = true;
          this._sendBufferedData = new PromiseSource();
          this._transportResult = new PromiseSource();
          this._sendLoopPromise = this._sendLoop();
        }
        _createClass(TransportSendQueue, [{
          key: "send",
          value: function send(data) {
            this._bufferData(data);
            if (!this._transportResult) {
              this._transportResult = new PromiseSource();
            }
            return this._transportResult.promise;
          }
        }, {
          key: "stop",
          value: function stop() {
            this._executing = false;
            this._sendBufferedData.resolve();
            return this._sendLoopPromise;
          }
        }, {
          key: "_bufferData",
          value: function _bufferData(data) {
            if (this._buffer.length && _typeof(this._buffer[0]) !== _typeof(data)) {
              throw new Error("Expected data to be of type ".concat(_typeof(this._buffer), " but was of type ").concat(_typeof(data)));
            }
            this._buffer.push(data);
            this._sendBufferedData.resolve();
          }
        }, {
          key: "_sendLoop",
          value: function () {
            var _sendLoop2 = _asyncToGenerator( /*#__PURE__*/_regeneratorRuntime().mark(function _callee21() {
              var transportResult, data;
              return _regeneratorRuntime().wrap(function _callee21$(_context22) {
                while (1) switch (_context22.prev = _context22.next) {
                  case 0:
                    if (!true) {
                      _context22.next = 22;
                      break;
                    }
                    _context22.next = 3;
                    return this._sendBufferedData.promise;
                  case 3:
                    if (this._executing) {
                      _context22.next = 6;
                      break;
                    }
                    if (this._transportResult) {
                      this._transportResult.reject("Connection stopped.");
                    }
                    return _context22.abrupt("break", 22);
                  case 6:
                    this._sendBufferedData = new PromiseSource();
                    transportResult = this._transportResult;
                    this._transportResult = undefined;
                    data = typeof this._buffer[0] === "string" ? this._buffer.join("") : TransportSendQueue._concatBuffers(this._buffer);
                    this._buffer.length = 0;
                    _context22.prev = 11;
                    _context22.next = 14;
                    return this._transport.send(data);
                  case 14:
                    transportResult.resolve();
                    _context22.next = 20;
                    break;
                  case 17:
                    _context22.prev = 17;
                    _context22.t0 = _context22["catch"](11);
                    transportResult.reject(_context22.t0);
                  case 20:
                    _context22.next = 0;
                    break;
                  case 22:
                  case "end":
                    return _context22.stop();
                }
              }, _callee21, this, [[11, 17]]);
            }));
            function _sendLoop() {
              return _sendLoop2.apply(this, arguments);
            }
            return _sendLoop;
          }()
        }], [{
          key: "_concatBuffers",
          value: function _concatBuffers(arrayBuffers) {
            var totalLength = arrayBuffers.map(function (b) {
              return b.byteLength;
            }).reduce(function (a, b) {
              return a + b;
            });
            var result = new Uint8Array(totalLength);
            var offset = 0;
            var _iterator6 = _createForOfIteratorHelper(arrayBuffers),
              _step6;
            try {
              for (_iterator6.s(); !(_step6 = _iterator6.n()).done;) {
                var item = _step6.value;
                result.set(new Uint8Array(item), offset);
                offset += item.byteLength;
              }
            } catch (err) {
              _iterator6.e(err);
            } finally {
              _iterator6.f();
            }
            return result.buffer;
          }
        }]);
        return TransportSendQueue;
      }();
      var PromiseSource = /*#__PURE__*/function () {
        function PromiseSource() {
          var _this30 = this;
          _classCallCheck(this, PromiseSource);
          this.promise = new Promise(function (resolve, reject) {
            var _ref2;
            return _ref2 = [resolve, reject], _this30._resolver = _ref2[0], _this30._rejecter = _ref2[1], _ref2;
          });
        }
        _createClass(PromiseSource, [{
          key: "resolve",
          value: function resolve() {
            this._resolver();
          }
        }, {
          key: "reject",
          value: function reject(reason) {
            this._rejecter(reason);
          }
        }]);
        return PromiseSource;
      }();
      ; // CONCATENATED MODULE: ./src/JsonHubProtocol.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      var JSON_HUB_PROTOCOL_NAME = "json";
      /** Implements the JSON Hub Protocol. */
      var _JsonHubProtocol = /*#__PURE__*/function () {
        function _JsonHubProtocol() {
          _classCallCheck(this, _JsonHubProtocol);
          /** @inheritDoc */
          this.name = JSON_HUB_PROTOCOL_NAME;
          /** @inheritDoc */
          this.version = 1;
          /** @inheritDoc */
          this.transferFormat = _TransferFormat.Text;
        }
        /** Creates an array of {@link @microsoft/signalr.HubMessage} objects from the specified serialized representation.
         *
         * @param {string} input A string containing the serialized representation.
         * @param {ILogger} logger A logger that will be used to log messages that occur during parsing.
         */
        _createClass(_JsonHubProtocol, [{
          key: "parseMessages",
          value: function parseMessages(input, logger) {
            // The interface does allow "ArrayBuffer" to be passed in, but this implementation does not. So let's throw a useful error.
            if (typeof input !== "string") {
              throw new Error("Invalid input for JSON hub protocol. Expected a string.");
            }
            if (!input) {
              return [];
            }
            if (logger === null) {
              logger = _NullLogger.instance;
            }
            // Parse the messages
            var messages = TextMessageFormat.parse(input);
            var hubMessages = [];
            var _iterator7 = _createForOfIteratorHelper(messages),
              _step7;
            try {
              for (_iterator7.s(); !(_step7 = _iterator7.n()).done;) {
                var message = _step7.value;
                var parsedMessage = JSON.parse(message);
                if (typeof parsedMessage.type !== "number") {
                  throw new Error("Invalid payload.");
                }
                switch (parsedMessage.type) {
                  case _MessageType.Invocation:
                    this._isInvocationMessage(parsedMessage);
                    break;
                  case _MessageType.StreamItem:
                    this._isStreamItemMessage(parsedMessage);
                    break;
                  case _MessageType.Completion:
                    this._isCompletionMessage(parsedMessage);
                    break;
                  case _MessageType.Ping:
                    // Single value, no need to validate
                    break;
                  case _MessageType.Close:
                    // All optional values, no need to validate
                    break;
                  default:
                    // Future protocol changes can add message types, old clients can ignore them
                    logger.log(_LogLevel.Information, "Unknown message type '" + parsedMessage.type + "' ignored.");
                    continue;
                }
                hubMessages.push(parsedMessage);
              }
            } catch (err) {
              _iterator7.e(err);
            } finally {
              _iterator7.f();
            }
            return hubMessages;
          }
          /** Writes the specified {@link @microsoft/signalr.HubMessage} to a string and returns it.
           *
           * @param {HubMessage} message The message to write.
           * @returns {string} A string containing the serialized representation of the message.
           */
        }, {
          key: "writeMessage",
          value: function writeMessage(message) {
            return TextMessageFormat.write(JSON.stringify(message));
          }
        }, {
          key: "_isInvocationMessage",
          value: function _isInvocationMessage(message) {
            this._assertNotEmptyString(message.target, "Invalid payload for Invocation message.");
            if (message.invocationId !== undefined) {
              this._assertNotEmptyString(message.invocationId, "Invalid payload for Invocation message.");
            }
          }
        }, {
          key: "_isStreamItemMessage",
          value: function _isStreamItemMessage(message) {
            this._assertNotEmptyString(message.invocationId, "Invalid payload for StreamItem message.");
            if (message.item === undefined) {
              throw new Error("Invalid payload for StreamItem message.");
            }
          }
        }, {
          key: "_isCompletionMessage",
          value: function _isCompletionMessage(message) {
            if (message.result && message.error) {
              throw new Error("Invalid payload for Completion message.");
            }
            if (!message.result && message.error) {
              this._assertNotEmptyString(message.error, "Invalid payload for Completion message.");
            }
            this._assertNotEmptyString(message.invocationId, "Invalid payload for Completion message.");
          }
        }, {
          key: "_assertNotEmptyString",
          value: function _assertNotEmptyString(value, errorMessage) {
            if (typeof value !== "string" || value === "") {
              throw new Error(errorMessage);
            }
          }
        }]);
        return _JsonHubProtocol;
      }();
      ; // CONCATENATED MODULE: ./src/HubConnectionBuilder.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      var LogLevelNameMapping = {
        trace: _LogLevel.Trace,
        debug: _LogLevel.Debug,
        info: _LogLevel.Information,
        information: _LogLevel.Information,
        warn: _LogLevel.Warning,
        warning: _LogLevel.Warning,
        error: _LogLevel.Error,
        critical: _LogLevel.Critical,
        none: _LogLevel.None
      };
      function parseLogLevel(name) {
        // Case-insensitive matching via lower-casing
        // Yes, I know case-folding is a complicated problem in Unicode, but we only support
        // the ASCII strings defined in LogLevelNameMapping anyway, so it's fine -anurse.
        var mapping = LogLevelNameMapping[name.toLowerCase()];
        if (typeof mapping !== "undefined") {
          return mapping;
        } else {
          throw new Error("Unknown log level: ".concat(name));
        }
      }
      /** A builder for configuring {@link @microsoft/signalr.HubConnection} instances. */
      var _HubConnectionBuilder = /*#__PURE__*/function () {
        function _HubConnectionBuilder() {
          _classCallCheck(this, _HubConnectionBuilder);
        }
        _createClass(_HubConnectionBuilder, [{
          key: "configureLogging",
          value: function configureLogging(logging) {
            Arg.isRequired(logging, "logging");
            if (isLogger(logging)) {
              this.logger = logging;
            } else if (typeof logging === "string") {
              var logLevel = parseLogLevel(logging);
              this.logger = new ConsoleLogger(logLevel);
            } else {
              this.logger = new ConsoleLogger(logging);
            }
            return this;
          }
        }, {
          key: "withUrl",
          value: function withUrl(url, transportTypeOrOptions) {
            Arg.isRequired(url, "url");
            Arg.isNotEmpty(url, "url");
            this.url = url;
            // Flow-typing knows where it's at. Since HttpTransportType is a number and IHttpConnectionOptions is guaranteed
            // to be an object, we know (as does TypeScript) this comparison is all we need to figure out which overload was called.
            if (_typeof(transportTypeOrOptions) === "object") {
              this.httpConnectionOptions = _objectSpread(_objectSpread({}, this.httpConnectionOptions), transportTypeOrOptions);
            } else {
              this.httpConnectionOptions = _objectSpread(_objectSpread({}, this.httpConnectionOptions), {}, {
                transport: transportTypeOrOptions
              });
            }
            return this;
          }
          /** Configures the {@link @microsoft/signalr.HubConnection} to use the specified Hub Protocol.
           *
           * @param {IHubProtocol} protocol The {@link @microsoft/signalr.IHubProtocol} implementation to use.
           */
        }, {
          key: "withHubProtocol",
          value: function withHubProtocol(protocol) {
            Arg.isRequired(protocol, "protocol");
            this.protocol = protocol;
            return this;
          }
        }, {
          key: "withAutomaticReconnect",
          value: function withAutomaticReconnect(retryDelaysOrReconnectPolicy) {
            if (this.reconnectPolicy) {
              throw new Error("A reconnectPolicy has already been set.");
            }
            if (!retryDelaysOrReconnectPolicy) {
              this.reconnectPolicy = new DefaultReconnectPolicy();
            } else if (Array.isArray(retryDelaysOrReconnectPolicy)) {
              this.reconnectPolicy = new DefaultReconnectPolicy(retryDelaysOrReconnectPolicy);
            } else {
              this.reconnectPolicy = retryDelaysOrReconnectPolicy;
            }
            return this;
          }
          /** Creates a {@link @microsoft/signalr.HubConnection} from the configuration options specified in this builder.
           *
           * @returns {HubConnection} The configured {@link @microsoft/signalr.HubConnection}.
           */
        }, {
          key: "build",
          value: function build() {
            // If httpConnectionOptions has a logger, use it. Otherwise, override it with the one
            // provided to configureLogger
            var httpConnectionOptions = this.httpConnectionOptions || {};
            // If it's 'null', the user **explicitly** asked for null, don't mess with it.
            if (httpConnectionOptions.logger === undefined) {
              // If our logger is undefined or null, that's OK, the HttpConnection constructor will handle it.
              httpConnectionOptions.logger = this.logger;
            }
            // Now create the connection
            if (!this.url) {
              throw new Error("The 'HubConnectionBuilder.withUrl' method must be called before building the connection.");
            }
            var connection = new HttpConnection(this.url, httpConnectionOptions);
            return _HubConnection.create(connection, this.logger || _NullLogger.instance, this.protocol || new _JsonHubProtocol(), this.reconnectPolicy);
          }
        }]);
        return _HubConnectionBuilder;
      }();
      function isLogger(logger) {
        return logger.log !== undefined;
      }
      ; // CONCATENATED MODULE: ./src/index.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.

      ; // CONCATENATED MODULE: ./src/browser-index.ts
      // Licensed to the .NET Foundation under one or more agreements.
      // The .NET Foundation licenses this file to you under the MIT license.
      // This is where we add any polyfills we'll need for the browser. It is the entry module for browser-specific builds.
      // Copy from Array.prototype into Uint8Array to polyfill on IE. It's OK because the implementations of indexOf and slice use properties
      // that exist on Uint8Array with the same name, and JavaScript is magic.
      // We make them 'writable' because the Buffer polyfill messes with it as well.
      if (!Uint8Array.prototype.indexOf) {
        Object.defineProperty(Uint8Array.prototype, "indexOf", {
          value: Array.prototype.indexOf,
          writable: true
        });
      }
      if (!Uint8Array.prototype.slice) {
        Object.defineProperty(Uint8Array.prototype, "slice", {
          // wrap the slice in Uint8Array so it looks like a Uint8Array.slice call
          // eslint-disable-next-line object-shorthand
          value: function value(start, end) {
            return new Uint8Array(Array.prototype.slice.call(this, start, end));
          },
          writable: true
        });
      }
      if (!Uint8Array.prototype.forEach) {
        Object.defineProperty(Uint8Array.prototype, "forEach", {
          value: Array.prototype.forEach,
          writable: true
        });
      }

      /******/
      return __webpack_exports__;
      /******/
    }()
  );
});
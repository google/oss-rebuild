// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package rebuild

import "google.golang.org/genai"

var TypeConversionBeforeStringConcatenation = &genai.Part{
	Text: `The category of the issue is "Type Conversion Before String Concatenation".
This happens when the diff contains a difference like:
- throw new AssertionError((Object)addPrefixToMessage(messagePrefix, "Unexpected exception: expected any of " + String.valueOf((Object)expectedExceptionSimpleNames) + ", actual = " + exceptionCaught.getClass().getSimpleName()));
+ throw new AssertionError((Object)addPrefixToMessage(messagePrefix, "Unexpected exception: expected any of " + expectedExceptionSimpleNames + ", actual = " + exceptionCaught.getClass().getSimpleName()));
he version before (-) converts each object into string first and then uses invokedynamic to concatenate strings.
The version (+) delegates the type conversion to invokedynamic also.
This is because of known fix implemented in JDK19 https://bugs.openjdk.org/browse/JDK-8273914.`,
}

var TypeConversionBeforeStringConcatenationFix = &genai.Part{
	Text: `Your goal is to match the upstream artifact which is indicated using '+' in the diff.
To fix the "Type Conversion Before String Concatenation" issue, you need to modify the JDK version in the build strategy.
Use JDK19 or later to ensure that the type conversion (String.valueOf) is done explicitly before string concatenation.
Use earlier JDK than 19, 17.0.14, and 11.0.27 to ensure that the type conversion is delegated to invokedynamic.`,
}

var EnumsDeclarationDifference = &genai.Part{
	Text: `The category of the issue is "Enums Declaration Difference"
This happens when the diff contains a difference like:
- Style.$VALUES = new Style[] { Style.SOAP11, Style.SOAP12, Style.XML };
+ Style.$VALUES = $values();
The version before (-) declares the enum values in-place.
The version (+) delegates the enum values declaration to a method call.
This is because of known enhancements introduced in JDK15 https://bugs.openjdk.org/browse/JDK-8241798.`,
}

var EnumsDeclarationDifferenceFix = &genai.Part{
	Text: `Your goal is to match the upstream artifact which is indicated using '+' in the diff.
To fix the "Enums Declaration Difference" issue, you need to modify the JDK version in the build strategy.
Use JDK15 or later to ensure that the enum values declaration is done using a method call.
Use earlier JDK than 15 to ensure that the enum values declaration is done in-place.`,
}

var AnonymousClassDifference = &genai.Part{
	Text: `The category of the issue is "Anonymous Class Difference".
This happens when the diff contains a difference like:
-class LoadErrorHandler$1 implements LoadErrorHandler {
+static final class LoadErrorHandler$1 implements LoadErrorHandler {
The version before (-) declares the anonymous class as a non-static inner class.
The version (+) declares the anonymous class as a static inner class.
This is because of known bug fix done in 9 https://bugs.openjdk.org/browse/JDK-8161009.`,
}

var AnonymousClassDifferenceFix = &genai.Part{
	Text: `Your goal is to match the upstream artifact which is indicated using '+' in the diff.
To fix the "Anonymous Class Difference" issue, you need to modify the JDK version in the build strategy.
Use JDK9 or later to ensure that the anonymous class is declared as a static inner class.
Use earlier JDK than 9 to ensure that the anonymous class is declared as a non-static inner class.`,
}

var DuplicateClassReferences = &genai.Part{
	Text: `The category of the issue is "Duplicate Class References".
This happens when the diff contains repeated instances of the same class reference in the constant pool like:
+   #110 = Class              #164          // java/lang/Boolean
+   #118 = Class              #164          // java/lang/Boolean
In the above example, java/lang/Boolean is referenced multiple times in the constant pool of generated class in upstream artifact.
This is happens in JDK earlier than 9 because of a known bug https://bugs.openjdk.org/browse/JDK-8015927/`,
}

var DuplicateClassReferencesFix = &genai.Part{
	Text: `Your goal is to match the upstream artifact which is indicated using '+' in the diff.
To fix the "Duplicate Class References" issue, you need to modify the JDK version in the build strategy.
Use JDK8 to ensure that the duplicate class references are present in the constant pool of generated classes.
Use JDK9 or later to ensure that the duplicate class references are eliminated from the constant pool of generated classes.`,
}

var RemovalOfObjectsRequiresNonNull = &genai.Part{
	Text: `The category of the issue is "Removal of Objects.requireNonNull".
This happens when the diff contains a difference like:
- errors.subList(1, errors.size());
- final Throwable obj;
- Objects.requireNonNull(obj);
- final Iterable iterable;
- iterable.forEach(obj::addSuppressed);
+ errors.subList(1, errors.size()).forEach(rootError::addSuppressed);
In JDK 9 and later, the Objects.requireNonNull call is introduced.
This is because of known enhancement introduced in JDK9 https://bugs.openjdk.org/browse/JDK-8074306.`,
}

var RemovalOfObjectsRequiresNonNullFix = &genai.Part{
	Text: `Your goal is to match the upstream artifact which is indicated using '+' in the diff.
To fix the "Removal of Objects.requireNonNull" issue, you need to modify the JDK version in the build strategy.
Use JDK9 or later to ensure that the Objects.requireNonNull call is introduced.
Use earlier JDK than 9 to ensure that the Objects.requireNonNull call is not present.`,
}
